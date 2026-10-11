/*
Copyright 2026 The KubeVela Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package spokecluster

import (
	"fmt"

	"github.com/crossplane/crossplane-runtime/pkg/event"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// Event reasons beyond the condition-reason constants. Detached is delete-path only;
// the two gateway-Secret reasons are registration-path only.
const (
	reasonDetached             = "Detached"
	reasonGatewaySecretCreated = "GatewaySecretCreated"
	reasonGatewaySecretUpdated = "GatewaySecretUpdated"
)

// emit records an event when a recorder is wired. Unit tests that build a Reconciler
// without SetupWithManager leave record nil and stay silent.
func (r *Reconciler) emit(obj runtime.Object, e event.Event) {
	if r == nil || r.record == nil {
		return
	}
	r.record.Event(obj, e)
}

// emitStatusEvents fires Kubernetes events only on meaningful status transitions so a
// steadily connected spoke on a 30s probe interval does not flood the Events API.
//
// Transition table:
//   - Connection -> Connected: Normal ProbeSucceeded
//   - Connection -> Disconnected: Warning ProbeFailed
//   - CredentialValid False (new): Warning MaterializeFailed / NoProvider / SpecInvalid
//   - Registered False (new): Warning RegisterFailed
//   - InfoSynced False (new): Warning DiscoveryFailed
//   - InfraProvisioned True, or False/Provisioning or False/Releasing (new): Normal
//     InfraReady / Provisioning / Releasing
//   - InfraProvisioned False, any other reason (new): Warning BlueprintUnresolved /
//     InfraRenderFailed / InfraUnhealthy / InfraUnmanagedExists / ReleaseRefused /
//     SpecInvalid
//   - InfraProvisioned removed after a release: Normal InfraReleased, emitted from
//     releaseInfra itself because there is no condition left to compare
//   - Bootstrapped True, or False/Bootstrapping (new): Normal Bootstrapped / Bootstrapping
//   - Bootstrapped False, any other reason (new): Warning BootstrapFailed /
//     BootstrapPlaneUnresolved
//   - Bootstrapped removed after spec.bootstrap was dropped: Normal BootstrapReleased,
//     emitted from forgetBootstrap itself for the same reason as InfraReleased
//   - Dispatched True, or False/Dispatching or False/WaitingForBootstrap (new): Normal
//     Dispatched / Dispatching / WaitingForBootstrap
//   - Dispatched False, any other reason (new): Warning DispatchFailed /
//     BlueprintUnresolved
//   - Dispatched removed after spec.blueprintRef was dropped: Normal DispatchReleased,
//     emitted from forgetDispatch itself
//   - SpokeReady -> True: Normal SpokeReady. Its False reasons are silent: not-ready is
//     the normal state of a spoke still provisioning, and ClusterAbsent is the steady
//     state of every connect-only spoke
//   - A pull that failed for any reason other than NotFound: Warning
//     SpokeStatusPullFailed, emitted from pullSpokeStatus itself on every such pass
//     because the condition is deliberately left unchanged
//
// Connection -> Unknown deliberately has no event of its own. It is always the
// consequence of a credential or registration failure that already emitted, so a second
// event would double-report one cause.
func (r *Reconciler) emitStatusEvents(sc *v1beta1.SpokeCluster, prev, next *v1beta1.SpokeClusterStatus) {
	if next == nil {
		return
	}
	var prevConn v1beta1.ConnectionState
	if prev != nil {
		prevConn = prev.Connection
	}
	if next.Connection != prevConn {
		switch next.Connection {
		case v1beta1.ConnectionStateConnected:
			r.emit(sc, event.Normal(reasonProbeSucceeded, conditionMessage(next, v1beta1.SpokeClusterConditionConnected)))
			countConnectionTransition(sc, v1beta1.ConnectionStateConnected)
		case v1beta1.ConnectionStateDisconnected:
			msg := conditionMessage(next, v1beta1.SpokeClusterConditionConnected)
			r.emit(sc, event.Warning(reasonProbeFailed, fmt.Errorf("%s", msg)))
			countConnectionTransition(sc, v1beta1.ConnectionStateDisconnected)
		}
	}

	emitWarningOnConditionFalse(r, sc, prev, next, v1beta1.SpokeClusterConditionCredentialValid)
	emitWarningOnConditionFalse(r, sc, prev, next, v1beta1.SpokeClusterConditionRegistered)
	emitWarningOnConditionFalse(r, sc, prev, next, v1beta1.SpokeClusterConditionInfoSynced)
	emitInfraProvisionedEvent(r, sc, prev, next)
	emitBootstrappedEvent(r, sc, prev, next)
	emitDispatchedEvent(r, sc, prev, next)
	emitSpokeReadyEvent(r, sc, prev, next)
}

// emitBootstrappedEvent reports runtime installation progress and failures, on the same
// model as emitInfraProvisionedEvent: Bootstrapping is the normal in-flight state and
// reaching True is progress, both Normal; BootstrapFailed and BootstrapPlaneUnresolved
// are Warnings and count as condition failures. Each transition reports once, so a
// runtime stuck installing on a 30s probe interval does not flood the Events API.
func emitBootstrappedEvent(r *Reconciler, sc *v1beta1.SpokeCluster, prev, next *v1beta1.SpokeClusterStatus) {
	emitProgressOrFault(r, sc, prev, next, v1beta1.SpokeClusterConditionBootstrapped, reasonBootstrapping)
}

// emitInfraProvisionedEvent reports provisioning progress and failures. Provisioning
// and Releasing are the normal in-flight states and are Normal events; every other
// False reason (BlueprintUnresolved, InfraRenderFailed, InfraUnhealthy,
// InfraUnmanagedExists, ReleaseRefused, SpecInvalid) is a Warning and counts as a
// condition failure, so a permanent misconfiguration is visible in events and metrics
// rather than only in the condition.
func emitInfraProvisionedEvent(r *Reconciler, sc *v1beta1.SpokeCluster, prev, next *v1beta1.SpokeClusterStatus) {
	emitProgressOrFault(r, sc, prev, next, v1beta1.SpokeClusterConditionInfraProvisioned, reasonProvisioning, reasonReleasing)
}

// emitDispatchedEvent reports bundle shipping on the same model: Dispatching and
// WaitingForBootstrap are in flight, True is progress, DispatchFailed and
// BlueprintUnresolved are faults.
func emitDispatchedEvent(r *Reconciler, sc *v1beta1.SpokeCluster, prev, next *v1beta1.SpokeClusterStatus) {
	emitProgressOrFault(r, sc, prev, next, v1beta1.SpokeClusterConditionDispatched, reasonDispatching, reasonWaitingForBootstrap)
}

// emitSpokeReadyEvent reports only the arrival at True. See emitStatusEvents for why the
// False reasons stay silent.
func emitSpokeReadyEvent(r *Reconciler, sc *v1beta1.SpokeCluster, prev, next *v1beta1.SpokeClusterStatus) {
	nextCond, changed := conditionTransition(prev, next, v1beta1.SpokeClusterConditionSpokeReady)
	if !changed || nextCond.Status != metav1.ConditionTrue {
		return
	}
	r.emit(sc, event.Normal(event.Reason(nextCond.Reason), nextCond.Message))
}

// emitProgressOrFault is the shared transition model for the render conditions: a new
// True, or a new False carrying one of inFlight, is a Normal event; any other new False
// is a Warning and counts as a condition failure. An unchanged status and reason emits
// nothing, so a condition stuck on a 30s probe interval reports once.
func emitProgressOrFault(r *Reconciler, sc *v1beta1.SpokeCluster, prev, next *v1beta1.SpokeClusterStatus, condType string, inFlight ...string) {
	nextCond, changed := conditionTransition(prev, next, condType)
	if !changed {
		return
	}
	normal := nextCond.Status == metav1.ConditionTrue
	for _, reason := range inFlight {
		normal = normal || nextCond.Reason == reason
	}
	if normal {
		r.emit(sc, event.Normal(event.Reason(nextCond.Reason), nextCond.Message))
		return
	}
	r.emit(sc, event.Warning(event.Reason(nextCond.Reason), fmt.Errorf("%s", nextCond.Message)))
	spokeConditionFailures.WithLabelValues(sc.Namespace, sc.Name, condType, nextCond.Reason).Inc()
}

// conditionTransition returns next's condition of condType and whether its status or
// reason differs from prev's. A condition absent from next is never a transition.
func conditionTransition(prev, next *v1beta1.SpokeClusterStatus, condType string) (*metav1.Condition, bool) {
	nextCond := meta.FindStatusCondition(next.Conditions, condType)
	if nextCond == nil {
		return nil, false
	}
	var prevCond *metav1.Condition
	if prev != nil {
		prevCond = meta.FindStatusCondition(prev.Conditions, condType)
	}
	if prevCond != nil && prevCond.Status == nextCond.Status && prevCond.Reason == nextCond.Reason {
		return nextCond, false
	}
	return nextCond, true
}

func countConnectionTransition(sc *v1beta1.SpokeCluster, to v1beta1.ConnectionState) {
	spokeConnectionTransitions.WithLabelValues(sc.Namespace, sc.Name, string(to)).Inc()
}

// emitGatewaySecretEvent reports what registration did to the materialized gateway
// Secret. An unchanged rewrite stays silent: with the credential cache serving a stable
// token, identical content is the steady state for a healthy spoke, and an event per pass
// would bury the transitions that matter.
func (r *Reconciler) emitGatewaySecretEvent(sc *v1beta1.SpokeCluster, outcome registerOutcome) {
	switch outcome {
	case registerCreated:
		r.emit(sc, event.Normal(reasonGatewaySecretCreated, "gateway credential secret created"))
	case registerUpdated:
		r.emit(sc, event.Normal(reasonGatewaySecretUpdated, "gateway credential secret rewritten with new content"))
	case registerUnchanged:
	}
}

func emitWarningOnConditionFalse(r *Reconciler, sc *v1beta1.SpokeCluster, prev, next *v1beta1.SpokeClusterStatus, condType string) {
	nextCond := meta.FindStatusCondition(next.Conditions, condType)
	if nextCond == nil || nextCond.Status != metav1.ConditionFalse {
		return
	}
	prevCond := (*metav1.Condition)(nil)
	if prev != nil {
		prevCond = meta.FindStatusCondition(prev.Conditions, condType)
	}
	if prevCond != nil && prevCond.Status == metav1.ConditionFalse && prevCond.Reason == nextCond.Reason {
		return
	}
	r.emit(sc, event.Warning(event.Reason(nextCond.Reason), fmt.Errorf("%s", nextCond.Message)))
	spokeConditionFailures.WithLabelValues(sc.Namespace, sc.Name, condType, nextCond.Reason).Inc()
}

func conditionMessage(status *v1beta1.SpokeClusterStatus, condType string) string {
	if status == nil {
		return ""
	}
	cond := meta.FindStatusCondition(status.Conditions, condType)
	if cond == nil || cond.Message == "" {
		return string(condType)
	}
	return cond.Message
}
