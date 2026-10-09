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
	"strings"

	"github.com/crossplane/crossplane-runtime/pkg/event"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// Release reasons. Stable strings, like the connect and provision reasons.
const (
	reasonReleasing      = "Releasing"
	reasonReleaseRefused = "ReleaseRefused"
	reasonInfraReleased  = "InfraReleased"
)

// releaseInfra runs when a SpokeCluster that used to manage infrastructure (status.provisioning
// is set) is now in mode connect. It deletes the infra Application so vela-core, following the
// Application's retain rule, strips its labels from the substrate objects instead of deleting
// them, then forgets the infrastructure and continues as a plain connect spoke. Without the
// retain rule the deletion would destroy the cluster, so release is refused until the user
// sets infraDeletionPolicy retain in the previous mode.
func (r *Reconciler) releaseInfra(ctx context.Context, sc *v1beta1.SpokeCluster) (ctrl.Result, error) {
	status := sc.Status.DeepCopy()
	status.ObservedGeneration = sc.Generation

	app := &v1beta1.Application{}
	err := r.Get(ctx, client.ObjectKey{Namespace: sc.Namespace, Name: infraAppName(sc)}, app)
	switch {
	case apierrors.IsNotFound(err):
		// Already gone, whether this pass or an earlier one deleted it: fall through to
		// forgetting the infrastructure.
	case err != nil:
		return ctrl.Result{}, err
	default:
		// Only an Application this SpokeCluster created may be deleted on its behalf. A
		// same-named Application owned by something else (or by nothing) is someone
		// else's cluster, and deleting it would tear that cluster down.
		if !metav1.IsControlledBy(app, sc) {
			msg := "infra Application " + app.Name + " is not owned by this SpokeCluster; refusing to delete it"
			setCondition(status, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonReleaseRefused, msg)
			markConnectionUnobserved(status, reasonReleaseRefused, msg)
			return r.finish(ctx, sc, status, probeInterval(sc), nil)
		}
		if !hasRetainRule(app) {
			msg := "infra Application has no retain rule (infraDeletionPolicy was delete); releasing it would destroy the cluster. Set infraDeletionPolicy retain in the previous mode first"
			setCondition(status, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonReleaseRefused, msg)
			markConnectionUnobserved(status, reasonReleaseRefused, msg)
			return r.finish(ctx, sc, status, probeInterval(sc), nil)
		}
		if app.DeletionTimestamp.IsZero() {
			// Stripped before the delete so a failure here leaves the Application in
			// place and the pass retries with backoff, rather than leaving half-marked
			// objects behind an Application that is already going away.
			if err := r.stripVelaMarkers(ctx, app); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.Delete(ctx, app); err != nil && !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
			klog.InfoS("Releasing infra Application", "spokecluster", klog.KObj(sc), "application", app.Name)
		}
		// vela-core still has to process the deletion and strip its labels, so this pass
		// stops here and the Owns watch wakes the loop when the Application is gone.
		// status.connection and the Connected condition are deliberately left alone: the
		// spoke is as reachable as it was a pass ago, only who owns its infrastructure is
		// changing, and reporting Unknown would read as an outage that never happened.
		setCondition(status, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonReleasing,
			"releasing substrate objects: the infra Application is being deleted under its retain rule")
		return r.finish(ctx, sc, status, provisionRequeue, nil)
	}

	// Written directly rather than through finish: the connect sequence that follows
	// starts from sc.Status, and it must see the projection gone or a no-op connect pass
	// would carry the stale provisioning fields forward.
	status.Provisioning = nil
	meta.RemoveStatusCondition(&status.Conditions, v1beta1.SpokeClusterConditionInfraProvisioned)
	if err := r.updateStatus(ctx, sc, status); err != nil {
		return ctrl.Result{}, err
	}
	sc.Status = *status
	r.emit(sc, event.Normal(reasonInfraReleased, "substrate objects released; the hub no longer manages this cluster's infrastructure"))
	return r.reconcileConnect(ctx, sc)
}

// Marker keys vela-core and this controller stamp on applied resources. The name and
// namespace labels are deliberately absent: vela-core's own garbage collection removes
// them when the Application goes, and IsResourceManagedByApplication needs them until then.
const (
	velaLabelPrefix       = "app.oam.dev/"
	velaLabelName         = "app.oam.dev/name"
	velaLabelNamespace    = "app.oam.dev/namespace"
	velaAnnotationRender  = "oam.dev/render-hash"
	velaLabelWorkloadType = "workload.oam.dev/type"
	velaLabelTraitType    = "trait.oam.dev/type"
	velaLabelTraitRes     = "trait.oam.dev/resource"
)

// stripVelaMarkers removes every ownership marker except the name and namespace labels
// from the resources the infra Application applied. vela-core's garbage collection strips
// only app.oam.dev/name and app.oam.dev/namespace; it leaves the revision, revision hash,
// component and render-hash markers behind. An adopt Application created afterwards for
// the same cluster is revision v1 again and renders to the same hash, so vela-core sees
// an object that already carries its revision and render hash, skips the apply, and never
// restores the name label: the Application reports healthy while its resource tracker is
// empty and the objects belong to nobody. Clearing the markers here makes the next apply
// a real one. Only hub-local resources are touched; a reference into another cluster is
// not a substrate object this controller rendered.
func (r *Reconciler) stripVelaMarkers(ctx context.Context, app *v1beta1.Application) error {
	for _, ref := range app.Status.AppliedResources {
		if ref.Cluster != "" && ref.Cluster != "local" {
			continue
		}
		if ref.APIVersion == "" || ref.Kind == "" || ref.Name == "" {
			continue
		}
		obj := &unstructured.Unstructured{}
		obj.SetAPIVersion(ref.APIVersion)
		obj.SetKind(ref.Kind)
		err := r.Get(ctx, client.ObjectKey{Namespace: ref.Namespace, Name: ref.Name}, obj)
		switch {
		case apierrors.IsNotFound(err):
			continue
		case err != nil:
			return fmt.Errorf("reading applied resource %s %s/%s: %w", ref.Kind, ref.Namespace, ref.Name, err)
		}
		labels, changedLabels := withoutVelaMarkers(obj.GetLabels())
		annotations, changedAnnotations := withoutVelaMarkers(obj.GetAnnotations())
		if !changedLabels && !changedAnnotations {
			continue
		}
		patched := obj.DeepCopy()
		patched.SetLabels(labels)
		patched.SetAnnotations(annotations)
		if err := r.Patch(ctx, patched, client.MergeFrom(obj)); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("stripping vela markers from %s %s/%s: %w", ref.Kind, ref.Namespace, ref.Name, err)
		}
		klog.V(1).InfoS("Stripped vela ownership markers from released resource",
			"application", klog.KObj(app), "kind", ref.Kind, "name", klog.KRef(ref.Namespace, ref.Name))
	}
	return nil
}

// withoutVelaMarkers returns in minus every marker key, and whether anything was removed.
// A nil map stays nil so a patch against an object without labels or annotations is empty.
func withoutVelaMarkers(in map[string]string) (map[string]string, bool) {
	var out map[string]string
	changed := false
	for k, v := range in {
		if isVelaMarker(k) {
			changed = true
			continue
		}
		if out == nil {
			out = map[string]string{}
		}
		out[k] = v
	}
	return out, changed
}

// isVelaMarker reports whether a label or annotation key is one release must clear. The
// two labels vela-core's garbage collection owns are excluded on purpose.
func isVelaMarker(key string) bool {
	switch key {
	case velaLabelName, velaLabelNamespace:
		return false
	case velaAnnotationRender, velaLabelWorkloadType, velaLabelTraitType, velaLabelTraitRes, labelSpokeName, labelSpokeRole:
		return true
	}
	return strings.HasPrefix(key, velaLabelPrefix)
}

// retainPolicy is the shape of a garbage-collect policy's properties that release cares
// about: which components each rule selects and what it does with their resources.
type retainPolicy struct {
	Rules []struct {
		Strategy string `json:"strategy"`
		Selector struct {
			ComponentNames []string `json:"componentNames"`
		} `json:"selector"`
	} `json:"rules"`
}

// hasRetainRule reports whether every component of the infra Application is covered by a
// garbage-collect rule with strategy never, which is what makes deleting the Application
// safe for the cluster. A rule with no componentNames selects every component. The policy
// is parsed rather than string-matched so a rule that retains only some of the substrate
// objects, or one written by hand with different spacing, is judged on what it means.
func hasRetainRule(app *v1beta1.Application) bool {
	retained := map[string]bool{}
	for _, p := range app.Spec.Policies {
		if p.Type != "garbage-collect" || p.Properties == nil {
			continue
		}
		var policy retainPolicy
		if err := json.Unmarshal(p.Properties.Raw, &policy); err != nil {
			continue
		}
		for _, rule := range policy.Rules {
			if rule.Strategy != "never" {
				continue
			}
			if len(rule.Selector.ComponentNames) == 0 {
				return len(app.Spec.Components) > 0
			}
			for _, name := range rule.Selector.ComponentNames {
				retained[name] = true
			}
		}
	}
	if len(app.Spec.Components) == 0 {
		return false
	}
	for _, c := range app.Spec.Components {
		if !retained[c.Name] {
			return false
		}
	}
	return true
}
