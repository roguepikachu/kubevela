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
	"context"
	"time"

	recorder "github.com/crossplane/crossplane-runtime/pkg/event"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	oamctrl "github.com/oam-dev/kubevela/pkg/controller/core.oam.dev"
	"github.com/oam-dev/kubevela/pkg/features"
	"github.com/oam-dev/kubevela/pkg/spokecluster/credential"
)

const (
	// controllerName names the controller in events and log lines.
	controllerName = "SpokeCluster"

	// defaultProbeInterval and defaultProbeTimeout back up the schema defaults on
	// spec.probeIntervalSeconds and spec.probeTimeoutSeconds. Admission means an admitted
	// object always carries both, so these only ever apply to an object written before the
	// defaults landed, or built directly in a test.
	defaultProbeInterval = 30 * time.Second
	defaultProbeTimeout  = 10 * time.Second

	// minRequeue floors every requeue. A credential whose refresh deadline has already
	// passed would otherwise ask to be reconciled immediately, forever.
	minRequeue = 5 * time.Second
)

// Condition reasons. These are an operator-facing contract: they are what distinguishes an
// unreachable spoke from a broken credential, so they are stable strings, not prose.
const (
	reasonNoProvider         = "NoProvider"
	reasonMaterializeFailed  = "MaterializeFailed"
	reasonMaterialized       = "Materialized"
	reasonRegisterFailed     = "RegisterFailed"
	reasonSecretMaterialized = "SecretMaterialized"
	reasonProbeFailed        = "ProbeFailed"
	reasonProbeSucceeded     = "ProbeSucceeded"
	reasonDiscoveryFailed    = "DiscoveryFailed"
	reasonDiscoveryOK        = "DiscoveryOK"
)

// Reconcile brings one SpokeCluster's status in line with the spoke's live state.
//
// The order is fixed: fetch, deletion dispatch, finalizer, then connect. The finalizer is
// persisted before any external side effect, so a spoke that got as far as a gateway Secret
// always has teardown guaranteed. Adding it does trigger a follow-up reconcile, but this
// pass carries on into the connect sequence rather than returning early, so a first-time
// SpokeCluster reaches Connected in one pass instead of two.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.InfoS("Reconcile SpokeCluster", "spokecluster", klog.KRef(req.Namespace, req.Name))

	sc := &v1beta1.SpokeCluster{}
	if err := r.Get(ctx, req.NamespacedName, sc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !sc.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, sc)
	}

	if !controllerutil.ContainsFinalizer(sc, FinalizerName) {
		controllerutil.AddFinalizer(sc, FinalizerName)
		if err := r.Update(ctx, sc); err != nil {
			return ctrl.Result{}, err
		}
	}

	return r.reconcileConnect(ctx, sc)
}

// reconcileConnect runs the connect sequence and writes status exactly once, whichever step
// it got to. It stops at the first hard failure: no registration after a credential
// failure, no probe after a failed registration, no discovery after a failed probe, so the
// conditions read as a prefix of the sequence rather than a mix of stale and fresh.
//
// The status copy starts from the object's current status, which is what lets a discovery
// failure keep the last known good clusterInfo instead of blanking it.
func (r *Reconciler) reconcileConnect(ctx context.Context, sc *v1beta1.SpokeCluster) (ctrl.Result, error) {
	status := sc.Status.DeepCopy()
	status.ObservedGeneration = sc.Generation

	// Every failure before the probe reports Unknown rather than Disconnected: the spoke was
	// never reached, so its reachability is genuinely unobserved this pass. Disconnected
	// would claim we looked and found it down, and leaving a previous Connected in place
	// would keep asserting reachability the controller can no longer see.
	provider, err := r.Providers.For(sc.Spec.Credential.Type)
	if err != nil {
		setCondition(status, v1beta1.SpokeClusterConditionCredentialValid, metav1.ConditionFalse, reasonNoProvider, err.Error())
		status.Connection = v1beta1.ConnectionStateUnknown
		return r.finish(ctx, sc, status, 0, err)
	}

	materialized, err := provider.Materialize(ctx, r.Client, sc)
	if err != nil {
		setCondition(status, v1beta1.SpokeClusterConditionCredentialValid, metav1.ConditionFalse, reasonMaterializeFailed, err.Error())
		status.Connection = v1beta1.ConnectionStateUnknown
		return r.finish(ctx, sc, status, 0, err)
	}
	setCondition(status, v1beta1.SpokeClusterConditionCredentialValid, metav1.ConditionTrue, reasonMaterialized,
		"credential materialized for endpoint "+materialized.Endpoint)

	if err := r.register(ctx, sc, materialized); err != nil {
		setCondition(status, v1beta1.SpokeClusterConditionRegistered, metav1.ConditionFalse, reasonRegisterFailed, err.Error())
		status.Connection = v1beta1.ConnectionStateUnknown
		return r.finish(ctx, sc, status, 0, err)
	}
	setCondition(status, v1beta1.SpokeClusterConditionRegistered, metav1.ConditionTrue, reasonSecretMaterialized,
		"gateway secret is up to date")

	latency, probeErr := r.probeSpoke(ctx, sc)
	probedAt := metav1.Now()
	status.LastProbeTime = &probedAt
	if probeErr != nil {
		setCondition(status, v1beta1.SpokeClusterConditionConnected, metav1.ConditionFalse, reasonProbeFailed, probeErr.Error())
		status.Connection = v1beta1.ConnectionStateDisconnected
		// Deliberately no error: an unreachable spoke is state to report, not a controller
		// fault to back off on. The plain probe interval also skips the refresh cap, so a
		// disconnected spoke with an expiring credential can idle up to one interval past
		// expiry; harmless, because the next pass remints before probing.
		return r.finish(ctx, sc, status, probeInterval(sc), nil)
	}
	// The measured latency belongs in clusterInfo.latencyMillis, not in this message: a
	// message that changes every pass would make every status write a real write for no
	// added information, and it is already reported in a field with a printer column.
	setCondition(status, v1beta1.SpokeClusterConditionConnected, metav1.ConditionTrue, reasonProbeSucceeded,
		"spoke answered the healthz probe")
	status.Connection = v1beta1.ConnectionStateConnected

	info, discoverErr := r.discoverSpoke(ctx, sc, materialized, latency)
	if discoverErr != nil {
		// Inventory is not connectivity: a spoke that answers /healthz but refuses a node
		// list is still connected, so this never fails the pass.
		setCondition(status, v1beta1.SpokeClusterConditionInfoSynced, metav1.ConditionFalse, reasonDiscoveryFailed, discoverErr.Error())
	} else {
		setCondition(status, v1beta1.SpokeClusterConditionInfoSynced, metav1.ConditionTrue, reasonDiscoveryOK,
			"cluster inventory refreshed")
		status.ClusterInfo = info
	}

	return r.finish(ctx, sc, status, nextRequeue(sc, materialized), nil)
}

// probeSpoke dispatches to the probe seam when a test set one, and to the real probe
// otherwise.
func (r *Reconciler) probeSpoke(ctx context.Context, sc *v1beta1.SpokeCluster) (time.Duration, error) {
	if r.probeFn != nil {
		return r.probeFn(ctx, sc)
	}
	return r.probe(ctx, sc)
}

// discoverSpoke dispatches to the discovery seam when a test set one, and to the real
// discovery otherwise.
func (r *Reconciler) discoverSpoke(ctx context.Context, sc *v1beta1.SpokeCluster, m *credential.Materialized, latency time.Duration) (*v1beta1.SpokeClusterInfo, error) {
	if r.discoverFn != nil {
		return r.discoverFn(ctx, sc, m, latency)
	}
	return r.discover(ctx, sc, m, latency)
}

// finish ends every pass the same way: write the status that was computed, then either
// surface the failure for controller-runtime's exponential backoff or schedule the next
// pass. Status is written first so a failure is still visible to an operator even though
// the pass errors out.
func (r *Reconciler) finish(ctx context.Context, sc *v1beta1.SpokeCluster, status *v1beta1.SpokeClusterStatus, requeue time.Duration, reconcileErr error) (ctrl.Result, error) {
	if err := r.updateStatus(ctx, sc, status); err != nil {
		return ctrl.Result{}, err
	}
	if reconcileErr != nil {
		return ctrl.Result{}, reconcileErr
	}
	if requeue < minRequeue {
		requeue = minRequeue
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// updateStatus replaces status wholesale through the status subresource, retrying on
// conflict. Replacing rather than merging is safe because this controller is the only
// writer of SpokeCluster status: the hub pulls spoke state, the spoke pushes nothing.
func (r *Reconciler) updateStatus(ctx context.Context, sc *v1beta1.SpokeCluster, status *v1beta1.SpokeClusterStatus) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		latest := &v1beta1.SpokeCluster{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(sc), latest); err != nil {
			return err
		}
		latest.Status = *status
		return r.Status().Update(ctx, latest)
	})
}

// setCondition records one condition. meta.SetStatusCondition leaves LastTransitionTime
// alone when the status has not flipped, so a steadily connected spoke does not churn its
// conditions on every pass.
func setCondition(status *v1beta1.SpokeClusterStatus, condType string, condStatus metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:    condType,
		Status:  condStatus,
		Reason:  reason,
		Message: message,
	})
}

// probeInterval is how often a spoke is probed when nothing else forces an earlier pass.
func probeInterval(sc *v1beta1.SpokeCluster) time.Duration {
	if sc.Spec.ProbeIntervalSeconds > 0 {
		return time.Duration(sc.Spec.ProbeIntervalSeconds) * time.Second
	}
	return defaultProbeInterval
}

// nextRequeue is the probe interval, capped by the credential's own refresh deadline. That
// cap is what keeps an AWS spoke reconciling before its minted token expires, rather than
// on whatever cadence someone chose for probing. A static kubeconfig spoke reports a zero
// NextRefresh and so just follows the probe interval.
//
// The result can be zero or negative when the deadline has already passed; finish applies
// the floor.
func nextRequeue(sc *v1beta1.SpokeCluster, m *credential.Materialized) time.Duration {
	requeue := probeInterval(sc)
	if m == nil || m.NextRefresh.IsZero() {
		return requeue
	}
	if untilRefresh := time.Until(m.NextRefresh); untilRefresh < requeue {
		return untilRefresh
	}
	return requeue
}

// ignoreOwnStatusWrites keeps the controller from reacting to the status it just wrote.
//
// Every pass records a fresh lastProbeTime and a freshly measured latency, so every pass
// changes status, and each of those changes comes back as an update event that triggers
// another pass. Left unfiltered that multiplied the work: a single spoke on a 10 second
// probe interval reconciled about 26 times a minute instead of 6, probing the spoke every
// time, and the amplification scales with the fleet.
//
// The filter is written out rather than using predicate.GenerationChangedPredicate because
// generation does not move when a finalizer is added or when a deletion timestamp is set,
// and dropping the deletion event would leave teardown waiting on the next requeue instead
// of starting immediately. Cadence stays with RequeueAfter, which is where this controller
// wants it: status tracks live state on the probe interval, not on how often status changes.
var ignoreOwnStatusWrites = predicate.Funcs{
	UpdateFunc: func(e event.UpdateEvent) bool {
		if e.ObjectOld == nil || e.ObjectNew == nil {
			return true
		}
		if e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() {
			return true
		}
		// Compare whether the object is deleting, not the timestamp pointers: old and new
		// are distinct objects, so their pointers never match and comparing them directly
		// would let every update through once deletion had started.
		return (e.ObjectOld.GetDeletionTimestamp() != nil) != (e.ObjectNew.GetDeletionTimestamp() != nil)
	},
}

// SetupWithManager registers the reconciler with the manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.record = recorder.NewAPIRecorder(mgr.GetEventRecorderFor(controllerName)).
		WithAnnotations("controller", controllerName)
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: r.concurrentReconciles,
		}).
		For(&v1beta1.SpokeCluster{}, builder.WithPredicates(ignoreOwnStatusWrites)).
		Complete(r)
}

// Setup adds the SpokeCluster controller to the manager, unless the feature gate is off, in
// which case nothing is registered and the CRD stays inert.
//
// This runs in the separate vela-cluster-core manager (cmd/cluster-core), never in
// vela-core; see the note on pkg/controller/core.oam.dev/v1beta1.Setup.
func Setup(mgr ctrl.Manager, args oamctrl.Args) error {
	if !utilfeature.DefaultMutableFeatureGate.Enabled(features.EnableClusterInfrastructure) {
		klog.InfoS("SpokeCluster controller disabled because its feature gate is off",
			"gate", features.EnableClusterInfrastructure)
		return nil
	}
	r := &Reconciler{
		Client:               mgr.GetClient(),
		Scheme:               mgr.GetScheme(),
		Config:               mgr.GetConfig(),
		Providers:            credential.DefaultRegistry(),
		concurrentReconciles: args.ConcurrentReconciles,
	}
	return r.SetupWithManager(mgr)
}
