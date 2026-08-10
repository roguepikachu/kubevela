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
// CredentialExpiring is reserved for a future remint-before-probe signal.
const (
	reasonDetached = "Detached"
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
//   - CredentialValid False (new): Warning MaterializeFailed / NoProvider
//   - Registered False (new): Warning RegisterFailed
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
			spokeConnectionTransitions.WithLabelValues("Connected").Inc()
		case v1beta1.ConnectionStateDisconnected:
			msg := conditionMessage(next, v1beta1.SpokeClusterConditionConnected)
			r.emit(sc, event.Warning(reasonProbeFailed, fmt.Errorf("%s", msg)))
			spokeConnectionTransitions.WithLabelValues("Disconnected").Inc()
		}
	}

	emitWarningOnConditionFalse(r, sc, prev, next, v1beta1.SpokeClusterConditionCredentialValid)
	emitWarningOnConditionFalse(r, sc, prev, next, v1beta1.SpokeClusterConditionRegistered)
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
	spokeConditionFailures.WithLabelValues(condType, nextCond.Reason).Inc()
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
