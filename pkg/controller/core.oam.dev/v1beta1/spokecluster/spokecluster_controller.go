/*
 Copyright 2026. The KubeVela Authors.

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

// Package spokecluster reconciles the hub-side SpokeCluster resource for Connect Phase 1. It
// materializes connectivity from a pluggable credential provider, registers the spoke with
// cluster-gateway, probes it on demand, discovers its inventory, and reports status. The hub
// reads spoke state by pull; the spoke never pushes status back.
package spokecluster

import (
	"context"
	"time"

	"github.com/crossplane/crossplane-runtime/pkg/event"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	oamctrl "github.com/oam-dev/kubevela/pkg/controller/core.oam.dev"
	"github.com/oam-dev/kubevela/pkg/features"
	"github.com/oam-dev/kubevela/pkg/spokecluster/credential"
)

const (
	// FinalizerName guards deletion so the controller can detach the spoke before the CR is gone.
	FinalizerName = "spokecluster.core.oam.dev/finalizer"
	// controllerName is used for the event recorder and logging.
	controllerName = "SpokeCluster"
	// defaultProbeInterval is the fallback requeue when the spec value is unset.
	defaultProbeInterval = 30 * time.Second
	// minRequeue caps how quickly we requeue to avoid hot loops on rapid credential refresh.
	minRequeue = 5 * time.Second
)

// Reconciler reconciles a SpokeCluster object.
type Reconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Config    *rest.Config
	Providers credential.Registry
	record    event.Recorder

	concurrentReconciles int

	// Test seams. When nil they fall back to the real methods (probe, discover). Tests set them
	// to avoid touching a live spoke or a rest.Config.
	probeFn    func(ctx context.Context, sc *v1beta1.SpokeCluster) (time.Duration, error)
	discoverFn func(ctx context.Context, sc *v1beta1.SpokeCluster, m *credential.Materialized, latency time.Duration) (*v1beta1.SpokeClusterInfo, error)
}

// probeSpoke dispatches to the test seam when set, otherwise the real probe.
func (r *Reconciler) probeSpoke(ctx context.Context, sc *v1beta1.SpokeCluster) (time.Duration, error) {
	if r.probeFn != nil {
		return r.probeFn(ctx, sc)
	}
	return r.probe(ctx, sc)
}

// discoverSpoke dispatches to the test seam when set, otherwise the real discovery.
func (r *Reconciler) discoverSpoke(ctx context.Context, sc *v1beta1.SpokeCluster, m *credential.Materialized, latency time.Duration) (*v1beta1.SpokeClusterInfo, error) {
	if r.discoverFn != nil {
		return r.discoverFn(ctx, sc, m, latency)
	}
	return r.discover(ctx, sc, m, latency)
}

// Reconcile drives one SpokeCluster toward its desired connected state.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.InfoS("Reconcile SpokeCluster", "spokecluster", klog.KRef(req.Namespace, req.Name))

	sc := &v1beta1.SpokeCluster{}
	if err := r.Get(ctx, req.NamespacedName, sc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Deletion branch: run the finalizer cleanup then release.
	if !sc.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, sc)
	}

	// Ensure the finalizer is present before doing any external work.
	if !controllerutil.ContainsFinalizer(sc, FinalizerName) {
		controllerutil.AddFinalizer(sc, FinalizerName)
		if err := r.Update(ctx, sc); err != nil {
			return ctrl.Result{}, err
		}
	}

	return r.reconcileConnect(ctx, sc)
}

// reconcileConnect materializes credentials, registers the spoke, probes it, and discovers info.
func (r *Reconciler) reconcileConnect(ctx context.Context, sc *v1beta1.SpokeCluster) (ctrl.Result, error) {
	status := sc.Status.DeepCopy()
	status.ObservedGeneration = sc.Generation

	// 1. Resolve the credential provider and materialize connectivity.
	provider, err := r.Providers.For(sc.Spec.Credential.Type)
	if err != nil {
		setCondition(status, v1beta1.SpokeClusterConditionCredentialValid, metav1.ConditionFalse, "NoProvider", err.Error())
		return r.finish(ctx, sc, status, defaultProbeInterval, err)
	}
	materialized, err := provider.Materialize(ctx, r.Client, sc)
	if err != nil {
		setCondition(status, v1beta1.SpokeClusterConditionCredentialValid, metav1.ConditionFalse, "MaterializeFailed", err.Error())
		status.Connection = v1beta1.ConnectionStateUnknown
		return r.finish(ctx, sc, status, r.probeInterval(sc), err)
	}
	setCondition(status, v1beta1.SpokeClusterConditionCredentialValid, metav1.ConditionTrue, "Materialized", "credential resolved")

	// 2. Register the spoke with cluster-gateway by upserting the ownerRef'd gateway secret.
	if err := r.register(ctx, sc, materialized); err != nil {
		setCondition(status, v1beta1.SpokeClusterConditionRegistered, metav1.ConditionFalse, "RegisterFailed", err.Error())
		return r.finish(ctx, sc, status, r.probeInterval(sc), err)
	}
	setCondition(status, v1beta1.SpokeClusterConditionRegistered, metav1.ConditionTrue, "SecretMaterialized", "cluster-gateway secret written")

	// 3. Probe the spoke (pull) and record connectivity + latency.
	latency, probeErr := r.probeSpoke(ctx, sc)
	status.LastProbeTime = &metav1.Time{Time: time.Now()}
	if probeErr != nil {
		status.Connection = v1beta1.ConnectionStateDisconnected
		setCondition(status, v1beta1.SpokeClusterConditionConnected, metav1.ConditionFalse, "ProbeFailed", probeErr.Error())
		return r.finish(ctx, sc, status, r.probeInterval(sc), nil)
	}
	status.Connection = v1beta1.ConnectionStateConnected
	setCondition(status, v1beta1.SpokeClusterConditionConnected, metav1.ConditionTrue, "ProbeSucceeded", "spoke API server reachable")

	// 4. Discover inventory and populate clusterInfo.
	info, discErr := r.discoverSpoke(ctx, sc, materialized, latency)
	if discErr != nil {
		setCondition(status, v1beta1.SpokeClusterConditionInfoSynced, metav1.ConditionFalse, "DiscoveryFailed", discErr.Error())
	} else {
		status.ClusterInfo = info
		setCondition(status, v1beta1.SpokeClusterConditionInfoSynced, metav1.ConditionTrue, "DiscoveryOK", "cluster info synced")
	}

	return r.finish(ctx, sc, status, r.nextRequeue(sc, materialized), nil)
}

// finish writes status and computes the requeue result.
func (r *Reconciler) finish(ctx context.Context, sc *v1beta1.SpokeCluster, status *v1beta1.SpokeClusterStatus, requeue time.Duration, reconcileErr error) (ctrl.Result, error) {
	if err := r.patchStatus(ctx, sc, status); err != nil {
		return ctrl.Result{}, err
	}
	if reconcileErr != nil {
		// Surface the error to controller-runtime for backoff, but status is already recorded.
		return ctrl.Result{}, reconcileErr
	}
	if requeue < minRequeue {
		requeue = minRequeue
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// probeInterval returns the configured probe interval or the default.
func (r *Reconciler) probeInterval(sc *v1beta1.SpokeCluster) time.Duration {
	if sc.Spec.ProbeIntervalSeconds > 0 {
		return time.Duration(sc.Spec.ProbeIntervalSeconds) * time.Second
	}
	return defaultProbeInterval
}

// nextRequeue is the sooner of the probe interval and the credential refresh deadline.
func (r *Reconciler) nextRequeue(sc *v1beta1.SpokeCluster, m *credential.Materialized) time.Duration {
	interval := r.probeInterval(sc)
	if m != nil && !m.NextRefresh.IsZero() {
		untilRefresh := time.Until(m.NextRefresh)
		if untilRefresh < interval {
			interval = untilRefresh
		}
	}
	return interval
}

// patchStatus writes status with conflict retry.
func (r *Reconciler) patchStatus(ctx context.Context, sc *v1beta1.SpokeCluster, status *v1beta1.SpokeClusterStatus) error {
	return retry.RetryOnConflict(retry.DefaultBackoff, func() error {
		latest := &v1beta1.SpokeCluster{}
		if err := r.Get(ctx, client.ObjectKeyFromObject(sc), latest); err != nil {
			return err
		}
		latest.Status = *status
		return r.Status().Update(ctx, latest)
	})
}

// setCondition sets a metav1.Condition on the status using the standard apimachinery helper.
func setCondition(status *v1beta1.SpokeClusterStatus, condType string, s metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:    condType,
		Status:  s,
		Reason:  reason,
		Message: msg,
	})
}

// SetupWithManager wires the reconciler into the manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.record = event.NewAPIRecorder(mgr.GetEventRecorderFor(controllerName)).WithAnnotations("controller", controllerName)
	return ctrl.NewControllerManagedBy(mgr).
		WithOptions(controller.Options{MaxConcurrentReconciles: r.concurrentReconciles}).
		For(&v1beta1.SpokeCluster{}).
		Complete(r)
}

// Setup adds the SpokeCluster controller to the manager, gated by the EnableSpokeClusterCRD
// feature. When the gate is off, the controller is not registered and the CRD is inert.
func Setup(mgr ctrl.Manager, args oamctrl.Args) error {
	if !utilfeature.DefaultMutableFeatureGate.Enabled(features.EnableSpokeClusterCRD) {
		klog.InfoS("SpokeCluster controller disabled (feature gate off)", "gate", features.EnableSpokeClusterCRD)
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
