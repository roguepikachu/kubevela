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
	"encoding/json"
	"fmt"

	"github.com/crossplane/crossplane-runtime/pkg/event"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// Bootstrap condition reasons. Stable strings, like the connect and provision reasons.
const (
	reasonBootstrapping            = "Bootstrapping"
	reasonBootstrapped             = "Bootstrapped"
	reasonBootstrapFailed          = "BootstrapFailed"
	reasonBootstrapPlaneUnresolved = "BootstrapPlaneUnresolved"
	// reasonBootstrapReleased is event-only, like reasonInfraReleased: it marks the pass
	// that deleted the bootstrap Application after spec.bootstrap was removed.
	reasonBootstrapReleased = "BootstrapReleased"
)

const roleBootstrap = "bootstrap"

// bootstrapAppName is deterministic per SpokeCluster name, with no UID or incarnation
// suffix. Each Connected pass re-applies the hub Application, which vela-core treats as a
// no-op while the render is unchanged; the spoke-side objects are kept in shape by
// vela-core's own periodic state-keep, not by this controller. So vela-core's
// skip-unchanged check doing nothing for an unchanged Application is exactly the right
// outcome here. The infra Application differs because adoption has to re-apply onto
// retained substrate objects whose stale revision markers would otherwise make vela-core
// skip them; see infraAppName. The base is truncated when needed so the whole stays a
// valid DNS-1123 label with the suffix intact and no hyphen left dangling at the cut.
func bootstrapAppName(sc *v1beta1.SpokeCluster) string {
	return dnsLabel("sc-"+sc.Name, "-bootstrap")
}

// reconcileBootstrap renders spec.bootstrap's ClusterPlane into one Application targeted
// at this spoke through cluster-gateway, and mirrors that Application's health into the
// Bootstrapped condition and status.bootstrap. It runs only on a pass that observed the
// spoke Connected, and it never returns an error: a runtime that will not install is state
// to report, and must not stop the hub from probing the spoke it is installing onto.
//
// The spec.bootstrap-unset arm is forgetBootstrap, which reconcileConnect runs before the
// probe so that removing the block takes effect on a spoke that is down.
//
// Every failure arm rewrites the projection too: a condition that says False next to a
// projection still claiming healthy from the last good pass would contradict itself.
func (r *Reconciler) reconcileBootstrap(ctx context.Context, sc *v1beta1.SpokeCluster, status *v1beta1.SpokeClusterStatus) {
	if sc.Spec.Bootstrap == nil {
		return
	}
	planeName := sc.Spec.Bootstrap.PlaneRef.Name
	if planeName == "" {
		projectBootstrapFailure(status, knownBootstrapAppName(status), reasonBootstrapPlaneUnresolved,
			"spec.bootstrap.planeRef.name is empty")
		return
	}
	plane := &v1beta1.ClusterPlane{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: sc.Namespace, Name: planeName}, plane); err != nil {
		projectBootstrapFailure(status, knownBootstrapAppName(status), reasonBootstrapPlaneUnresolved,
			fmt.Sprintf("reading ClusterPlane %s/%s: %v", sc.Namespace, planeName, err))
		return
	}
	comps := planeComponents(plane, "")
	if len(comps) == 0 {
		projectBootstrapFailure(status, knownBootstrapAppName(status), reasonBootstrapPlaneUnresolved,
			fmt.Sprintf("ClusterPlane %s has no components", planeName))
		return
	}

	name := bootstrapAppName(sc)
	// A terminating Application is left to finish. Mutating it would re-add labels and
	// spec to an object vela-core is tearing down, and CreateOrUpdate cannot recreate it
	// until the finalizers clear; the Owns watch wakes the loop when it is gone.
	existing := &v1beta1.Application{}
	switch err := r.Get(ctx, client.ObjectKey{Namespace: sc.Namespace, Name: name}, existing); {
	case apierrors.IsNotFound(err):
	case err != nil:
		projectBootstrapFailure(status, name, reasonBootstrapFailed,
			fmt.Sprintf("reading bootstrap Application %s/%s: %v", sc.Namespace, name, err))
		return
	case !existing.DeletionTimestamp.IsZero():
		projectBootstrapFailure(status, name, reasonBootstrapping, "previous bootstrap Application is terminating")
		status.Bootstrap.Phase = string(existing.Status.Phase)
		return
	}

	app, err := r.ensureBootstrapApplication(ctx, sc, comps)
	if err != nil {
		klog.ErrorS(err, "Bootstrap Application could not be applied", "spokecluster", klog.KObj(sc))
		projectBootstrapFailure(status, name, reasonBootstrapFailed, err.Error())
		return
	}

	healthy, summary := summarizeAppHealth(app, comps)
	status.Bootstrap = &v1beta1.ProvisioningStatus{
		ApplicationName: app.Name,
		Phase:           string(app.Status.Phase),
		Healthy:         healthy,
		Message:         summary,
	}
	switch {
	case appPhaseFailed(app.Status.Phase):
		setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapFailed, summary)
	case healthy:
		setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionTrue, reasonBootstrapped, summary)
	default:
		setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapping, summary)
	}
}

// projectBootstrapFailure sets the Bootstrapped condition to False and makes the
// projection agree with it: the Application name when one is known (empty otherwise),
// never healthy, and the same message. Phase is cleared because this pass did not read
// one; a caller that did may set it afterwards.
func projectBootstrapFailure(status *v1beta1.SpokeClusterStatus, appName, reason, msg string) {
	status.Bootstrap = &v1beta1.ProvisioningStatus{ApplicationName: appName, Healthy: false, Message: msg}
	setCondition(status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reason, msg)
}

// knownBootstrapAppName is the Application name an earlier pass recorded, or empty when
// no pass has rendered one yet. Used where this pass stops before touching the Application,
// so that the projection does not name an object that may not exist.
func knownBootstrapAppName(status *v1beta1.SpokeClusterStatus) string {
	if status.Bootstrap == nil {
		return ""
	}
	return status.Bootstrap.ApplicationName
}

// forgetBootstrap is what a spoke without spec.bootstrap does about a runtime it used to
// manage: delete the bootstrap Application if this SpokeCluster controls it, then drop the
// projection and the condition. A spoke that never had a bootstrap block has nothing to
// forget and costs no API call here.
//
// Deleting the Application here, on a spoke the hub can still reach, means vela-core's
// garbage collection does reach the spoke: under the Application's never rule it strips
// the app.oam.dev/name and app.oam.dev/namespace labels from the runtime objects and
// deletes nothing, so Flux and vela-core stay installed and merely stop being managed.
// That is a different path from deleting the whole SpokeCluster, for which see
// bootstrapPolicies.
func (r *Reconciler) forgetBootstrap(ctx context.Context, sc *v1beta1.SpokeCluster, status *v1beta1.SpokeClusterStatus) {
	if status.Bootstrap == nil && meta.FindStatusCondition(status.Conditions, v1beta1.SpokeClusterConditionBootstrapped) == nil {
		return
	}
	name := knownBootstrapAppName(status)
	if name == "" {
		name = bootstrapAppName(sc)
	}
	app := &v1beta1.Application{}
	err := r.Get(ctx, client.ObjectKey{Namespace: sc.Namespace, Name: name}, app)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		projectBootstrapFailure(status, name, reasonBootstrapFailed,
			fmt.Sprintf("reading bootstrap Application %s/%s for removal: %v", sc.Namespace, name, err))
		return
	// Only an Application this SpokeCluster created may be deleted on its behalf; a
	// same-named one owned by something else is left alone and the projection is still
	// dropped, because this spoke no longer claims it.
	case metav1.IsControlledBy(app, sc):
		if err := r.Delete(ctx, app); err != nil && !apierrors.IsNotFound(err) {
			projectBootstrapFailure(status, name, reasonBootstrapFailed,
				fmt.Sprintf("deleting bootstrap Application %s/%s: %v", sc.Namespace, name, err))
			return
		}
		klog.InfoS("Bootstrap Application deleted because spec.bootstrap was removed", "spokecluster", klog.KObj(sc), "application", name)
		r.emit(sc, event.Normal(reasonBootstrapReleased, "bootstrap Application released; the hub no longer manages this spoke's runtime"))
	}
	status.Bootstrap = nil
	meta.RemoveStatusCondition(&status.Conditions, v1beta1.SpokeClusterConditionBootstrapped)
}

// ensureBootstrapApplication creates or updates the owner-referenced bootstrap
// Application. The owner reference is what deletes the Application when the SpokeCluster
// goes; what happens to the runtime on the spoke then is described on bootstrapPolicies.
func (r *Reconciler) ensureBootstrapApplication(ctx context.Context, sc *v1beta1.SpokeCluster, comps []common.ApplicationComponent) (*v1beta1.Application, error) {
	policies, err := bootstrapPolicies(sc, comps)
	if err != nil {
		return nil, err
	}
	return r.ensureOwnedApplication(ctx, sc, bootstrapAppName(sc), roleBootstrap, comps, policies)
}

// bootstrapPolicies targets the Application at the spoke and keeps the runtime there. The
// topology policy names the spoke by its SpokeCluster name, which is the name registration
// gave its cluster-gateway Secret.
//
// The garbage-collect never rule is what keeps the runtime when the bootstrap Application
// is deleted on a live spoke (spec.bootstrap removed, see forgetBootstrap): vela-core
// reaches the spoke, strips the app.oam.dev labels from Flux and vela-core and deletes
// nothing. It is not what protects the runtime when the whole SpokeCluster is deleted.
// On that path deletion.go runs multicluster.DetachCluster, whose
// removeClusterFromResourceTrackers strips this spoke's entries from every ResourceTracker
// before the finalizer is released; the owner-referenced Application is then collected
// with nothing left to reach on the spoke, so the runtime keeps even its labels.
//
// No apply-once: each pass re-applies the hub Application (a no-op while unchanged), and
// spoke objects are kept in shape by vela-core's periodic state-keep, so drift is corrected
// rather than tolerated. No take-over: a runtime someone else installed is not the hub's
// to claim.
func bootstrapPolicies(sc *v1beta1.SpokeCluster, comps []common.ApplicationComponent) ([]v1beta1.AppPolicy, error) {
	topology, err := json.Marshal(map[string]any{"clusters": []string{sc.Name}})
	if err != nil {
		return nil, err
	}
	retain, err := retainPolicy("retain-runtime", comps)
	if err != nil {
		return nil, err
	}
	return []v1beta1.AppPolicy{
		{Name: "target-spoke", Type: "topology", Properties: &runtime.RawExtension{Raw: topology}},
		retain,
	}, nil
}
