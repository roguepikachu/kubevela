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
	"strings"

	"github.com/crossplane/crossplane-runtime/pkg/event"
	wfTypesv1alpha1 "github.com/kubevela/pkg/apis/oam/v1alpha1"
	workflowv1alpha1 "github.com/kubevela/workflow/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/multicluster"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/resourcetracker"
)

// Labels the SpokeCluster controller puts on the Application it owns. The UID label is
// what ties the Application back to one SpokeCluster object; the name label is what the
// legacy migration lists by.
const (
	labelSpokeName = "spokecluster.core.oam.dev/name"
	labelSpokeUID  = "spokecluster.core.oam.dev/uid"

	// dnsLabelMaxLen is the longest a DNS-1123 label, and so an Application name, may be.
	dnsLabelMaxLen = 63
)

// Fixed policy names of the one Application. Workflow steps reference policies by name,
// so these are part of the Application's contract, not just labels.
const (
	policyHub     = "hub"
	policySpoke   = "spoke"
	policyInfra   = "infra"
	policyRuntime = "runtime"
	policyKeep    = "keep"
	policyAdopt   = "adopt"
)

// Fixed workflow step names of the one Application. The status projections look steps up
// by these names, so a rename here is a rename in the projections too.
const (
	stepProvision = "provision"
	stepConnected = "connected"
	stepBootstrap = "bootstrap"
	stepPlatform  = "platform"

	stepTypeDeploy             = "deploy"
	stepTypeWaitSpokeConnected = "wait-spoke-connected"
	stepTypeDispatchBlueprint  = "dispatch-blueprint"
)

// localCluster is the name vela-core gives the hub itself in a topology policy.
const localCluster = "local"

// reasonApplicationReleased is event-only: it marks the pass that deleted the Application
// after the last spec block that needed one was removed from a connect spoke.
const reasonApplicationReleased = "ApplicationReleased"

// errApplicationTerminating is returned by ensureApplication while a previous Application
// of the same name is still being torn down: mutating it would re-add labels and spec to
// an object vela-core is deleting, and CreateOrUpdate cannot recreate it until the
// finalizers clear. The Owns watch wakes the loop when it is gone.
var errApplicationTerminating = errors.New("previous Application is terminating")

// applicationName is deterministic per SpokeCluster name: "sc-<name>", truncated to a
// DNS-1123 label. One SpokeCluster owns exactly one Application; which objects it renders
// and where depends on the mode and the optional spec blocks, not on the name. A
// controller restart therefore resumes the same Application, and vela-core treats an
// unchanged render as a no-op on every pass.
//
// The name carries neither the UID nor status.infraIncarnation any more. The incarnation
// counter used to give a re-adopted cluster a fresh Application name so vela-core would
// not skip retained substrate objects whose stale appRevision markers matched; see the
// note on InfraIncarnation in release.go for what that means now.
func applicationName(sc *v1beta1.SpokeCluster) string {
	return dnsLabel("sc-"+sc.Name, "")
}

// dnsLabel joins base and suffix into a DNS-1123 label. The suffix is kept whole, because
// it is what tells same-named objects apart; the base is truncated when needed, and a
// hyphen left dangling at the cut is trimmed so the result stays a valid label.
func dnsLabel(base, suffix string) string {
	if len(base)+len(suffix) > dnsLabelMaxLen {
		base = strings.TrimRight(base[:dnsLabelMaxLen-len(suffix)], "-")
	}
	return base + suffix
}

// appRender is everything one pass resolved for the Application: the infra plane
// components (provision and adopt only), the bootstrap plane components, and the
// blueprint revision and custom definitions the platform step ships. The optional parts
// record their own failure instead of failing the render, so an unreadable bootstrap
// plane is reported on Bootstrapped while the infra still provisions.
type appRender struct {
	infra   []common.ApplicationComponent
	runtime []common.ApplicationComponent
	// runtimeErr is set when spec.bootstrap is present but its plane could not be
	// resolved; runtime is then empty and Bootstrapped reports BootstrapPlaneUnresolved.
	runtimeErr error

	// revision is the ClusterBlueprintRevision the platform step dispatches, empty
	// without spec.blueprintRef or when the blueprint could not be resolved.
	revision    string
	definitions []string
	// platformErr and platformReason are set when spec.blueprintRef is present but the
	// revisions or definitions could not be resolved; Dispatched reports them.
	platformErr    error
	platformReason string
}

// empty reports whether there is nothing for the Application to do: no components to
// deploy and no revision to dispatch. A connect spoke with neither spec.bootstrap nor
// spec.blueprintRef renders empty and owns no Application.
func (rd *appRender) empty() bool {
	return len(rd.infra) == 0 && len(rd.runtime) == 0 && rd.revision == ""
}

func (rd *appRender) infraNames() []string   { return componentNames(rd.infra) }
func (rd *appRender) runtimeNames() []string { return componentNames(rd.runtime) }

func componentNames(comps []common.ApplicationComponent) []string {
	names := make([]string, 0, len(comps))
	for _, c := range comps {
		names = append(names, c.Name)
	}
	return names
}

// renderApplication resolves the spec into an appRender. The only hard error is an
// infra blueprint that cannot be resolved in provision or adopt mode: without the
// substrate there is nothing to render. The bootstrap plane and the platform blueprint
// are resolved best-effort and their failures recorded on the render.
func (r *Reconciler) renderApplication(ctx context.Context, sc *v1beta1.SpokeCluster) (*appRender, error) {
	rd := &appRender{}
	if sc.Spec.Mode == v1beta1.SpokeClusterModeProvision || sc.Spec.Mode == v1beta1.SpokeClusterModeAdopt {
		comps, err := r.resolveInfraComponents(ctx, sc)
		if err != nil {
			return nil, err
		}
		rd.infra = comps
	}
	if sc.Spec.Bootstrap != nil {
		comps, err := r.resolveBootstrapComponents(ctx, sc)
		if err != nil {
			rd.runtimeErr = err
		} else {
			rd.runtime = comps
		}
	}
	if sc.Spec.BlueprintRef != nil {
		bundle, err := r.ensureRevisions(ctx, sc)
		switch {
		case errors.Is(err, errBlueprintUnresolved):
			rd.platformErr, rd.platformReason = err, reasonBlueprintUnresolved
		case err != nil:
			rd.platformErr, rd.platformReason = err, reasonDispatchFailed
		default:
			defs, err := r.customDefinitionNames(ctx, sc.Namespace, bundle)
			if err != nil {
				rd.platformErr, rd.platformReason = err, reasonDispatchFailed
			} else {
				rd.revision = bundle.blueprint.Name
				rd.definitions = defs
			}
		}
	}
	return rd, nil
}

// spec is the Application spec the render produces. Components are the infra plane's
// followed by the bootstrap plane's, names unprefixed within each group. Policies and
// steps are present only when the group they serve is, except keep and adopt, which are
// always present. The deploy steps name an override only when both groups exist; with
// one group the topology alone selects everything.
func (rd *appRender) spec(sc *v1beta1.SpokeCluster) (v1beta1.ApplicationSpec, error) {
	infraNames, runtimeNames := rd.infraNames(), rd.runtimeNames()
	both := len(infraNames) > 0 && len(runtimeNames) > 0

	comps := make([]common.ApplicationComponent, 0, len(rd.infra)+len(rd.runtime))
	comps = append(comps, rd.infra...)
	comps = append(comps, rd.runtime...)

	var policies []v1beta1.AppPolicy
	var steps []wfTypesv1alpha1.WorkflowStep
	add := func(name, typ string, props map[string]any) error {
		raw, err := json.Marshal(props)
		if err != nil {
			return err
		}
		policies = append(policies, v1beta1.AppPolicy{Name: name, Type: typ, Properties: &runtime.RawExtension{Raw: raw}})
		return nil
	}
	step := func(name, typ string, props map[string]any) error {
		raw, err := json.Marshal(props)
		if err != nil {
			return err
		}
		steps = append(steps, wfTypesv1alpha1.WorkflowStep{WorkflowStepBase: wfTypesv1alpha1.WorkflowStepBase{
			Name: name, Type: typ, Properties: &runtime.RawExtension{Raw: raw},
		}})
		return nil
	}

	if len(infraNames) > 0 {
		if err := add(policyHub, "topology", map[string]any{"clusters": []string{localCluster}}); err != nil {
			return v1beta1.ApplicationSpec{}, err
		}
	}
	if len(runtimeNames) > 0 {
		if err := add(policySpoke, "topology", map[string]any{"clusters": []string{sc.Name}}); err != nil {
			return v1beta1.ApplicationSpec{}, err
		}
	}
	if both {
		// override's components field is required by its schema; an empty list patches
		// nothing and leaves selector as the only effect.
		if err := add(policyInfra, "override", map[string]any{"components": []any{}, "selector": infraNames}); err != nil {
			return v1beta1.ApplicationSpec{}, err
		}
		if err := add(policyRuntime, "override", map[string]any{"components": []any{}, "selector": runtimeNames}); err != nil {
			return v1beta1.ApplicationSpec{}, err
		}
	}
	if err := add(policyKeep, "garbage-collect", map[string]any{"rules": keepRules(sc, infraNames, runtimeNames)}); err != nil {
		return v1beta1.ApplicationSpec{}, err
	}
	// take-over with no rules lets the Application claim any object its components render
	// that belongs to no application. Mode adopt needs it for retained substrate objects.
	// For this POC it is present in every mode, so the one Application can also take over
	// what the three Applications it replaces applied before they were deleted.
	if err := add(policyAdopt, "take-over", map[string]any{}); err != nil {
		return v1beta1.ApplicationSpec{}, err
	}

	if len(infraNames) > 0 {
		p := []string{policyHub}
		if both {
			p = append(p, policyInfra)
		}
		if err := step(stepProvision, stepTypeDeploy, map[string]any{"policies": p}); err != nil {
			return v1beta1.ApplicationSpec{}, err
		}
	}
	if len(runtimeNames) > 0 || rd.revision != "" {
		if err := step(stepConnected, stepTypeWaitSpokeConnected, map[string]any{"name": sc.Name, "namespace": sc.Namespace}); err != nil {
			return v1beta1.ApplicationSpec{}, err
		}
	}
	if len(runtimeNames) > 0 {
		p := []string{policySpoke}
		if both {
			p = append(p, policyRuntime)
		}
		if err := step(stepBootstrap, stepTypeDeploy, map[string]any{"policies": p}); err != nil {
			return v1beta1.ApplicationSpec{}, err
		}
	}
	if rd.revision != "" {
		defs := rd.definitions
		if defs == nil {
			defs = []string{}
		}
		if err := step(stepPlatform, stepTypeDispatchBlueprint, map[string]any{
			"cluster": sc.Name, "namespace": sc.Namespace, "revision": rd.revision, "definitions": defs,
		}); err != nil {
			return v1beta1.ApplicationSpec{}, err
		}
	}

	return v1beta1.ApplicationSpec{
		Components: comps,
		Policies:   policies,
		Workflow:   &v1beta1.Workflow{Steps: steps},
	}, nil
}

// keepRules are the garbage-collect rules that make deleting the Application safe for
// what it rendered: a never rule over the component names, and a never rule over the
// kinds the platform step dispatches to the spoke (revisions, definitions and the Cluster
// CR), which belong to no component. Under infraDeletionPolicy delete the infra components
// are left out of the first rule, so the owner-reference deletion of the Application
// cascades to the substrate objects; hasRetainRule in release.go reads that back.
func keepRules(sc *v1beta1.SpokeCluster, infraNames, runtimeNames []string) []map[string]any {
	kept := make([]string, 0, len(infraNames)+len(runtimeNames))
	if sc.Spec.InfraDeletionPolicy != v1beta1.InfraDeletionPolicyDelete {
		kept = append(kept, infraNames...)
	}
	kept = append(kept, runtimeNames...)
	var rules []map[string]any
	if len(kept) > 0 {
		rules = append(rules, map[string]any{
			"selector": map[string]any{"componentNames": kept},
			"strategy": "never",
		})
	}
	return append(rules, map[string]any{
		"selector": map[string]any{"resourceTypes": []string{"Cluster", "ClusterBlueprintRevision", "ClusterPlaneRevision", "ComponentDefinition"}},
		"strategy": "never",
	})
}

// ensureApplication creates or updates the one Application the SpokeCluster controls:
// labelled back to the spoke by name and UID, carrying exactly spec, with sc as its
// controller reference. SetControllerReference refuses an Application another controller
// already owns, so a same-named foreign Application surfaces as an error rather than
// being taken over. A terminating Application is left to finish; see
// errApplicationTerminating.
func (r *Reconciler) ensureApplication(ctx context.Context, sc *v1beta1.SpokeCluster, spec v1beta1.ApplicationSpec) (*v1beta1.Application, error) {
	name := applicationName(sc)
	existing := &v1beta1.Application{}
	switch err := r.Get(ctx, client.ObjectKey{Namespace: sc.Namespace, Name: name}, existing); {
	case apierrors.IsNotFound(err):
	case err != nil:
		return nil, fmt.Errorf("reading Application %s/%s: %w", sc.Namespace, name, err)
	case !existing.DeletionTimestamp.IsZero():
		return nil, errApplicationTerminating
	}

	app := &v1beta1.Application{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sc.Namespace}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, app, func() error {
		if app.Labels == nil {
			app.Labels = map[string]string{}
		}
		app.Labels[labelSpokeName] = sc.Name
		app.Labels[labelSpokeUID] = string(sc.UID)
		app.Spec.Components = spec.Components
		app.Spec.Policies = spec.Policies
		app.Spec.Workflow = spec.Workflow
		return controllerutil.SetControllerReference(sc, app, r.Scheme)
	})
	if err != nil {
		return nil, fmt.Errorf("applying Application %s/%s: %w", sc.Namespace, name, err)
	}
	if op != controllerutil.OperationResultNone {
		klog.InfoS("Application reconciled", "spokecluster", klog.KObj(sc), "application", app.Name, "op", op)
	}
	return app, nil
}

// deleteLegacyApplications removes the per-role Applications an earlier controller
// rendered for this SpokeCluster (infra, bootstrap and dispatch, labelled by spoke name
// under other names) and reports whether any still exists. Their garbage-collect never
// rules keep the objects; the one Application then takes them over through its adopt
// policy. take-over only claims objects that belong to no application, so the caller
// must not render the new Application while a legacy one still owns them.
func (r *Reconciler) deleteLegacyApplications(ctx context.Context, sc *v1beta1.SpokeCluster) (bool, error) {
	apps := &v1beta1.ApplicationList{}
	if err := r.List(ctx, apps, client.InNamespace(sc.Namespace), client.MatchingLabels{labelSpokeName: sc.Name}); err != nil {
		return false, fmt.Errorf("listing Applications of SpokeCluster %s: %w", klog.KObj(sc), err)
	}
	keep := applicationName(sc)
	pending := false
	for i := range apps.Items {
		app := &apps.Items[i]
		if app.Name == keep || !metav1.IsControlledBy(app, sc) {
			continue
		}
		pending = true
		if !app.DeletionTimestamp.IsZero() {
			continue
		}
		if err := r.releaseLegacyResources(ctx, app); err != nil {
			return true, fmt.Errorf("releasing objects of legacy Application %s/%s: %w", app.Namespace, app.Name, err)
		}
		if err := r.Delete(ctx, app); err != nil && !apierrors.IsNotFound(err) {
			return true, fmt.Errorf("deleting legacy Application %s/%s: %w", app.Namespace, app.Name, err)
		}
		klog.InfoS("Legacy Application deleted; its objects are kept and the one Application takes them over",
			"spokecluster", klog.KObj(sc), "application", app.Name)
	}
	return pending, nil
}

// legacyLabels and legacyAnnotations are what a legacy Application leaves on every object
// it applied. The name and namespace labels are what makes take-over refuse the object as
// managed by another application; the appRevision label and the last-applied annotation
// are what would make vela-core skip it as already applied under the new Application.
var (
	legacyLabels      = []string{oam.LabelAppName, oam.LabelAppNamespace, oam.LabelAppRevision}
	legacyAnnotations = []string{oam.AnnotationLastAppliedConfig}
)

// releaseLegacyResources strips the application markers from every object a legacy
// Application still tracks, on the hub and on the spoke, before the Application is
// deleted. It walks the root and versioned ResourceTrackers (the component-revision
// tracker holds no applied objects) through the multicluster client, which reaches a
// spoke-side object through the gateway when the managed resource names a cluster and
// the hub otherwise. vela-core's own garbage collection under the never rule strips the
// name and namespace labels too, but only where it can still reach, and never the
// revision markers; doing it here makes the hand-over independent of that.
func (r *Reconciler) releaseLegacyResources(ctx context.Context, app *v1beta1.Application) error {
	rootRT, currentRT, historyRTs, _, err := resourcetracker.ListApplicationResourceTrackers(ctx, r.SpokeReader, app)
	if err != nil {
		return err
	}
	rts := append([]*v1beta1.ResourceTracker{rootRT, currentRT}, historyRTs...)
	for _, rt := range rts {
		if rt == nil {
			continue
		}
		for _, mr := range rt.Spec.ManagedResources {
			if mr.Deleted {
				continue
			}
			obj := mr.ToUnstructured()
			cctx := multicluster.ContextWithClusterName(ctx, mr.Cluster)
			switch err := r.SpokeReader.Get(cctx, client.ObjectKeyFromObject(obj), obj); {
			case apierrors.IsNotFound(err):
				continue
			case err != nil:
				return fmt.Errorf("reading %s: %w", mr.DisplayName(), err)
			}
			if !stripLegacyMarkers(obj) {
				continue
			}
			if err := r.SpokeReader.Update(cctx, obj); err != nil {
				return fmt.Errorf("stripping application markers from %s: %w", mr.DisplayName(), err)
			}
			klog.V(2).InfoS("Application markers stripped from legacy object", "application", klog.KObj(app), "object", mr.DisplayName())
		}
	}
	return nil
}

// stripLegacyMarkers removes legacyLabels and legacyAnnotations from obj and reports
// whether anything changed.
func stripLegacyMarkers(obj client.Object) bool {
	changed := false
	labels := obj.GetLabels()
	for _, k := range legacyLabels {
		if _, ok := labels[k]; ok {
			delete(labels, k)
			changed = true
		}
	}
	annotations := obj.GetAnnotations()
	for _, k := range legacyAnnotations {
		if _, ok := annotations[k]; ok {
			delete(annotations, k)
			changed = true
		}
	}
	if changed {
		obj.SetLabels(labels)
		obj.SetAnnotations(annotations)
	}
	return changed
}

// forgetApplication is what a connect spoke with nothing left to render does about an
// Application it used to own: delete it if this SpokeCluster controls it, then drop the
// bootstrap and dispatch projections. Under the keep policy vela-core strips its labels
// from the spoke objects and deletes nothing, so a forgotten spoke keeps the runtime and
// the blueprint it has. The delete is attempted on every pass whether or not status
// remembers an Application: a crash between CreateOrUpdate and the status write leaves an
// owned Application that status knows nothing about.
func (r *Reconciler) forgetApplication(ctx context.Context, sc *v1beta1.SpokeCluster, status *v1beta1.SpokeClusterStatus) {
	name := applicationName(sc)
	app := &v1beta1.Application{}
	err := r.Get(ctx, client.ObjectKey{Namespace: sc.Namespace, Name: name}, app)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		projectApplicationError(sc, status, fmt.Sprintf("reading Application %s/%s for removal: %v", sc.Namespace, name, err))
		return
	case metav1.IsControlledBy(app, sc) && app.DeletionTimestamp.IsZero():
		if err := r.Delete(ctx, app); err != nil && !apierrors.IsNotFound(err) {
			projectApplicationError(sc, status, fmt.Sprintf("deleting Application %s/%s: %v", sc.Namespace, name, err))
			return
		}
		klog.InfoS("Owned Application deleted because nothing is left to render", "spokecluster", klog.KObj(sc), "application", name)
		r.emit(sc, event.Normal(reasonApplicationReleased, "Application released; the spoke keeps what it has and the hub manages nothing on it"))
	}
	status.Bootstrap = nil
	status.DispatchedRevision = ""
	meta.RemoveStatusCondition(&status.Conditions, v1beta1.SpokeClusterConditionBootstrapped)
	meta.RemoveStatusCondition(&status.Conditions, v1beta1.SpokeClusterConditionDispatched)
}

// projectApplicationError reports a failure to read, apply or remove the Application on
// every projection the spec asks for, since all of them depend on that one object.
func projectApplicationError(sc *v1beta1.SpokeCluster, status *v1beta1.SpokeClusterStatus, msg string) {
	if sc.Spec.Bootstrap != nil {
		projectBootstrapFailure(status, applicationName(sc), reasonBootstrapFailed, msg)
	}
	if sc.Spec.BlueprintRef != nil {
		setCondition(status, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, reasonDispatchFailed, msg)
	}
}

// workflowStep is the status of the step called name, or nil when the workflow has not
// recorded it (not started yet, or the step is not part of this render).
func workflowStep(app *v1beta1.Application, name string) *workflowv1alpha1.WorkflowStepStatus {
	if app == nil || app.Status.Workflow == nil {
		return nil
	}
	for i := range app.Status.Workflow.Steps {
		if app.Status.Workflow.Steps[i].Name == name {
			return &app.Status.Workflow.Steps[i]
		}
	}
	return nil
}

func stepFailed(step *workflowv1alpha1.WorkflowStepStatus) bool {
	return step != nil && step.Phase == workflowv1alpha1.WorkflowStepPhaseFailed
}

func stepSucceeded(step *workflowv1alpha1.WorkflowStepStatus) bool {
	return step != nil && step.Phase == workflowv1alpha1.WorkflowStepPhaseSucceeded
}

// stepMessage appends the step's message to summary when the step has one.
func stepMessage(summary string, step *workflowv1alpha1.WorkflowStepStatus) string {
	if step == nil || step.Message == "" {
		return summary
	}
	return summary + "; step " + step.Name + ": " + step.Message
}

// componentsHealthy reports whether every component in names is healthy on the given
// cluster, with a one-line summary for the condition message. Services are matched by
// the names this pass rendered, so a stale entry left behind by a component since
// removed from the plane cannot make the count add up. An empty cluster in the service
// means the hub, which vela-core also reports as local.
func componentsHealthy(app *v1beta1.Application, names []string, cluster string) (bool, string) {
	expected := make(map[string]struct{}, len(names))
	for _, n := range names {
		expected[n] = struct{}{}
	}
	onCluster := func(c string) bool {
		if cluster == localCluster {
			return c == "" || c == localCluster
		}
		return c == cluster
	}
	healthy := 0
	var msgs []string
	for _, s := range app.Status.Services {
		if _, ok := expected[s.Name]; !ok || !onCluster(s.Cluster) {
			continue
		}
		if s.Healthy {
			healthy++
		}
		if s.Message != "" {
			msgs = append(msgs, s.Name+": "+s.Message)
		}
	}
	sort.Strings(msgs)
	summary := fmt.Sprintf("%d/%d components healthy, application phase %q", healthy, len(expected), app.Status.Phase)
	if len(msgs) > 0 {
		summary += "; " + strings.Join(msgs, "; ")
	}
	return len(expected) > 0 && healthy == len(expected), summary
}
