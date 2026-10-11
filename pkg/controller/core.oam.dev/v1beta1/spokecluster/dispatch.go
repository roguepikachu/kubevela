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
	"errors"
	"fmt"
	"sort"

	"github.com/crossplane/crossplane-runtime/pkg/event"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// Dispatch condition reasons. BlueprintUnresolved is shared with provisioning (see
// provision.go): both mean the referenced blueprint or a plane could not be read.
const (
	reasonDispatching         = "Dispatching"
	reasonDispatched          = "Dispatched"
	reasonDispatchFailed      = "DispatchFailed"
	reasonWaitingForBootstrap = "WaitingForBootstrap"
	// reasonDispatchReleased is event-only, like reasonBootstrapReleased: it marks the
	// pass that deleted the dispatch Application after spec.blueprintRef was removed.
	reasonDispatchReleased = "DispatchReleased"
)

const roleDispatch = "dispatch"

// Names of the three bundle components, in dispatch order. The Cluster goes last so the
// spoke-side controller finds its revisions and definitions already in place.
const (
	compRevisions   = "revisions"
	compDefinitions = "definitions"
	compCluster     = "cluster"
)

// builtinComponentTypes are the component types every vela-core ships. A plane that uses
// one needs nothing dispatched for it; a same-named ComponentDefinition in the hub
// namespace is somebody's local override and stays home.
var builtinComponentTypes = map[string]struct{}{
	"k8s-objects": {}, "raw": {}, "ref-objects": {}, "helmchart": {}, "webservice": {},
	"worker": {}, "task": {}, "cron-task": {}, "daemon": {}, "statefulset": {},
}

// dispatchAppName is deterministic per SpokeCluster name, for the reasons spelled out on
// bootstrapAppName: the hub Application is re-applied each Connected pass and vela-core
// treats an unchanged render as a no-op.
func dispatchAppName(sc *v1beta1.SpokeCluster) string {
	return dnsLabel("sc-"+sc.Name, "-dispatch")
}

// reconcileDispatch ships spec.blueprintRef to the spoke: it pins the blueprint and its
// planes as revisions, renders them, the custom ComponentDefinitions they use and a spoke
// Cluster CR into one Application targeted at this spoke through cluster-gateway, and
// mirrors that Application's health into the Dispatched condition. It runs only on a pass
// that observed the spoke Connected and never returns an error, like reconcileBootstrap:
// a bundle that will not ship is state to report, not a reason to stop probing.
//
// A spoke that is also being bootstrapped waits for Bootstrapped=True first: the bundle
// is applied by vela-core on the spoke, so there is nothing to receive it before then.
//
// The spec.blueprintRef-unset arm is forgetDispatch, which reconcileConnect runs before
// the probe so that removing the field takes effect on a spoke that is down.
func (r *Reconciler) reconcileDispatch(ctx context.Context, sc *v1beta1.SpokeCluster, status *v1beta1.SpokeClusterStatus) {
	if sc.Spec.BlueprintRef == nil {
		return
	}
	if sc.Spec.Bootstrap != nil && !meta.IsStatusConditionTrue(status.Conditions, v1beta1.SpokeClusterConditionBootstrapped) {
		setCondition(status, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, reasonWaitingForBootstrap,
			"waiting for the spoke runtime to be Bootstrapped before shipping the blueprint")
		return
	}
	bundle, err := r.ensureRevisions(ctx, sc)
	if err != nil {
		reason := reasonDispatchFailed
		if errors.Is(err, errBlueprintUnresolved) {
			reason = reasonBlueprintUnresolved
		}
		setCondition(status, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, reason, err.Error())
		return
	}
	comps, err := r.dispatchComponents(ctx, sc, bundle)
	if err != nil {
		setCondition(status, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, reasonDispatchFailed, err.Error())
		return
	}
	policies, err := targetedPolicies(sc, "retain-bundle", comps)
	if err != nil {
		setCondition(status, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, reasonDispatchFailed, err.Error())
		return
	}
	app, err := r.ensureOwnedApplication(ctx, sc, dispatchAppName(sc), roleDispatch, comps, policies)
	if err != nil {
		klog.ErrorS(err, "Dispatch Application could not be applied", "spokecluster", klog.KObj(sc))
		setCondition(status, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, reasonDispatchFailed, err.Error())
		return
	}

	status.DispatchedRevision = bundle.blueprint.Name
	healthy, summary := summarizeAppHealth(app, comps)
	summary = fmt.Sprintf("revision %s: %s", bundle.blueprint.Name, summary)
	switch {
	case appPhaseFailed(app.Status.Phase):
		setCondition(status, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, reasonDispatchFailed, summary)
	case healthy:
		setCondition(status, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionTrue, reasonDispatched, summary)
	default:
		setCondition(status, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, reasonDispatching, summary)
	}
}

// forgetDispatch is what a spoke without spec.blueprintRef does about a bundle it used
// to ship: delete the dispatch Application if this SpokeCluster controls it, then drop
// the recorded revision and the condition. Under the Application's never rule vela-core
// strips its labels from the revisions, definitions and Cluster on the spoke and deletes
// nothing, so a forgotten spoke keeps running the blueprint it has; it merely stops
// receiving new revisions. status.health is left to pullSpokeStatus, which keeps
// reporting whatever Cluster the spoke still has.
func (r *Reconciler) forgetDispatch(ctx context.Context, sc *v1beta1.SpokeCluster, status *v1beta1.SpokeClusterStatus) {
	ok := r.forgetOwnedApplication(ctx, sc, dispatchAppName(sc), roleDispatch,
		func(msg string) {
			setCondition(status, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, reasonDispatchFailed, msg)
		},
		func() {
			r.emit(sc, event.Normal(reasonDispatchReleased, "dispatch Application released; the spoke keeps the blueprint it has and receives no new revisions"))
		})
	if !ok {
		return
	}
	status.DispatchedRevision = ""
	meta.RemoveStatusCondition(&status.Conditions, v1beta1.SpokeClusterConditionDispatched)
}

// forgetOwnedApplication deletes the Application called name if this SpokeCluster
// controls it, and reports true when the caller may drop its projection. It does not
// consult status to decide whether there is anything to delete: a crash between
// CreateOrUpdate and the status write leaves an owned Application that status knows
// nothing about, and the cached Get this costs on every pass is what finds it.
//
// Only an Application this SpokeCluster created may be deleted on its behalf; a
// same-named one owned by something else is left alone and the caller still drops its
// projection, because this spoke no longer claims it. A read or delete failure goes to
// onFail with the message to report and returns false so the projection stands; a
// completed delete calls onReleased so the caller can say so in an event.
func (r *Reconciler) forgetOwnedApplication(ctx context.Context, sc *v1beta1.SpokeCluster, name, role string, onFail func(msg string), onReleased func()) bool {
	app := &v1beta1.Application{}
	err := r.Get(ctx, client.ObjectKey{Namespace: sc.Namespace, Name: name}, app)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		onFail(fmt.Sprintf("reading %s Application %s/%s for removal: %v", role, sc.Namespace, name, err))
		return false
	case metav1.IsControlledBy(app, sc):
		if err := r.Delete(ctx, app); err != nil && !apierrors.IsNotFound(err) {
			onFail(fmt.Sprintf("deleting %s Application %s/%s: %v", role, sc.Namespace, name, err))
			return false
		}
		klog.InfoS("Owned Application deleted because its spec block was removed", "spokecluster", klog.KObj(sc), "application", name, "role", role)
		onReleased()
	}
	return true
}

// dispatchComponents renders the bundle as k8s-objects components: the revisions, the
// custom ComponentDefinitions the planes use (omitted when there are none) and the spoke
// Cluster CR, which depends on the others so the spoke-side controller never sees a
// Cluster whose revisions have not landed.
func (r *Reconciler) dispatchComponents(ctx context.Context, sc *v1beta1.SpokeCluster, bundle *dispatchBundle) ([]common.ApplicationComponent, error) {
	revisions := make([]client.Object, 0, 1+len(bundle.planes))
	revisions = append(revisions, bundle.blueprint)
	for _, p := range bundle.planes {
		revisions = append(revisions, p)
	}
	revComp, err := r.objectsComponent(compRevisions, revisions, nil)
	if err != nil {
		return nil, err
	}
	comps := []common.ApplicationComponent{revComp}
	clusterDeps := []string{compRevisions}

	defs, err := r.customDefinitions(ctx, sc.Namespace, bundle)
	if err != nil {
		return nil, err
	}
	if len(defs) > 0 {
		defComp, err := r.objectsComponent(compDefinitions, defs, nil)
		if err != nil {
			return nil, err
		}
		comps = append(comps, defComp)
		clusterDeps = append(clusterDeps, compDefinitions)
	}

	clusterComp, err := r.objectsComponent(compCluster, []client.Object{spokeClusterObject(sc, bundle.blueprint.Name)}, clusterDeps)
	if err != nil {
		return nil, err
	}
	return append(comps, clusterComp), nil
}

// spokeClusterObject is the Cluster CR the spoke runs: named after the SpokeCluster, in
// the same namespace as on the hub, every lifecycle phase pinned to the one blueprint
// revision being shipped. The spoke-side controller picks its planes per phase from the
// revision, so one reference serves all three.
func spokeClusterObject(sc *v1beta1.SpokeCluster, blueprintRevision string) *v1beta1.Cluster {
	phase := &v1beta1.PhaseSpec{BlueprintRef: v1beta1.BlueprintReference{Name: blueprintRevision}}
	return &v1beta1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: sc.Name, Namespace: sc.Namespace},
		Spec: v1beta1.ClusterSpec{
			ClusterInit:       phase,
			PlaneProvisioning: phase.DeepCopy(),
			HealthValidation:  phase.DeepCopy(),
		},
	}
}

// customDefinitions reads, in name order, every ComponentDefinition in ns whose name is a
// component type some plane uses and that is not a vela-core built-in. A type with no
// definition in ns is skipped rather than failing the dispatch: it may be an addon type
// the spoke already has, and the spoke's own render is where a truly unknown type fails.
func (r *Reconciler) customDefinitions(ctx context.Context, ns string, bundle *dispatchBundle) ([]client.Object, error) {
	types := map[string]struct{}{}
	for _, p := range bundle.planes {
		for _, c := range p.Spec.Components {
			if _, builtin := builtinComponentTypes[c.Type]; !builtin {
				types[c.Type] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(types))
	for t := range types {
		names = append(names, t)
	}
	sort.Strings(names)
	defs := make([]client.Object, 0, len(names))
	for _, name := range names {
		def := &v1beta1.ComponentDefinition{}
		switch err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, def); {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return nil, fmt.Errorf("reading ComponentDefinition %s/%s: %w", ns, name, err)
		}
		defs = append(defs, def)
	}
	return defs, nil
}

// objectsComponent renders objs as one k8s-objects component with the given dependsOn.
func (r *Reconciler) objectsComponent(name string, objs []client.Object, dependsOn []string) (common.ApplicationComponent, error) {
	trimmed := make([]map[string]any, 0, len(objs))
	for _, obj := range objs {
		o, err := r.trimmedObject(obj)
		if err != nil {
			return common.ApplicationComponent{}, err
		}
		trimmed = append(trimmed, o)
	}
	raw, err := json.Marshal(map[string]any{"objects": trimmed})
	if err != nil {
		return common.ApplicationComponent{}, err
	}
	return common.ApplicationComponent{
		Name:       name,
		Type:       "k8s-objects",
		Properties: &runtime.RawExtension{Raw: raw},
		DependsOn:  dependsOn,
	}, nil
}

// trimmedObject is obj reduced to what the spoke should receive: apiVersion and kind from
// the scheme (a typed Get leaves TypeMeta empty), name, namespace and labels, and spec.
// Status, resourceVersion, UID, owner references and managed fields are hub-side state
// that must not be applied onto another cluster.
func (r *Reconciler) trimmedObject(obj client.Object) (map[string]any, error) {
	gvk, err := apiutil.GVKForObject(obj, r.Scheme)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var full map[string]any
	if err := json.Unmarshal(raw, &full); err != nil {
		return nil, err
	}
	metadata := map[string]any{"name": obj.GetName(), "namespace": obj.GetNamespace()}
	if labels := obj.GetLabels(); len(labels) > 0 {
		metadata["labels"] = labels
	}
	return map[string]any{
		"apiVersion": gvk.GroupVersion().String(),
		"kind":       gvk.Kind,
		"metadata":   metadata,
		"spec":       full["spec"],
	}, nil
}

// targetedPolicies targets an Application at the spoke and keeps what it rendered there.
// The topology policy names the spoke by its SpokeCluster name, which is the name
// registration gave its cluster-gateway Secret. The garbage-collect never rule, named
// retainName, is what keeps the spoke objects when the Application is deleted on a live
// spoke: vela-core reaches the spoke, strips the app.oam.dev labels and deletes nothing.
//
// No apply-once: each pass re-applies the hub Application (a no-op while unchanged), and
// spoke objects are kept in shape by vela-core's periodic state-keep, so drift is
// corrected rather than tolerated. No take-over: objects someone else put on the spoke
// are not the hub's to claim.
func targetedPolicies(sc *v1beta1.SpokeCluster, retainName string, comps []common.ApplicationComponent) ([]v1beta1.AppPolicy, error) {
	topology, err := json.Marshal(map[string]any{"clusters": []string{sc.Name}})
	if err != nil {
		return nil, err
	}
	retain, err := retainPolicy(retainName, comps)
	if err != nil {
		return nil, err
	}
	return []v1beta1.AppPolicy{
		{Name: "target-spoke", Type: "topology", Properties: &runtime.RawExtension{Raw: topology}},
		retain,
	}, nil
}
