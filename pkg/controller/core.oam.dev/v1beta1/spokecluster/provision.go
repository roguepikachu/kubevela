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
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	spokeadmission "github.com/oam-dev/kubevela/pkg/webhook/core.oam.dev/v1beta1/spokecluster"
)

// Provision condition reasons. Stable strings, like the connect reasons.
const (
	reasonBlueprintUnresolved = "BlueprintUnresolved"
	reasonInfraRenderFailed   = "InfraRenderFailed"
	reasonProvisioning        = "Provisioning"
	reasonInfraUnhealthy      = "InfraUnhealthy"
	reasonInfraReady          = "InfraReady"
)

const (
	// provisionRequeue is how often a provisioning spoke re-reads its infra Application.
	// EKS takes ten to twenty minutes; the Owns watch wakes the loop sooner on status
	// changes, so this is a backstop rather than the cadence.
	provisionRequeue = 30 * time.Second

	labelSpokeName = "spokecluster.core.oam.dev/name"
	labelSpokeRole = "spokecluster.core.oam.dev/role"
	roleInfra      = "infra"
)

// infraAppName is deterministic so a controller restart mid-provision resumes the same
// Application instead of rendering a second cluster.
func infraAppName(sc *v1beta1.SpokeCluster) string {
	return "sc-" + sc.Name + "-infra"
}

// reconcileProvision renders the infraProvisioning blueprint into one Application on the
// hub, reads its health back, and hands over to the connect sequence only once every
// substrate component is healthy. Before that the cluster does not exist, so a connect
// pass would spend an sts:AssumeRole and an eks:DescribeCluster to learn nothing.
func (r *Reconciler) reconcileProvision(ctx context.Context, sc *v1beta1.SpokeCluster) (ctrl.Result, error) {
	status := sc.Status.DeepCopy()
	status.ObservedGeneration = sc.Generation

	// A stored object that admission would reject must never create cloud
	// infrastructure, whether the validating webhook was Ignore during bootstrap or
	// disabled outright. The connect path enforces the same principle after hand-over;
	// checking here as well means a bad credential fails before a cluster is rendered
	// for it rather than after.
	if errs := spokeadmission.Validate(sc); len(errs) > 0 {
		msg := errs.ToAggregate().Error()
		setCondition(status, v1beta1.SpokeClusterConditionCredentialValid, metav1.ConditionFalse, reasonSpecInvalid, msg)
		setCondition(status, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonSpecInvalid, msg)
		markConnectionUnobserved(status, reasonSpecInvalid, msg)
		return r.finish(ctx, sc, status, probeInterval(sc), nil)
	}

	comps, err := r.resolveInfraComponents(ctx, sc)
	if err != nil {
		setCondition(status, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonBlueprintUnresolved, err.Error())
		markConnectionUnobserved(status, reasonBlueprintUnresolved, err.Error())
		return r.finish(ctx, sc, status, provisionRequeue, nil)
	}

	app, err := r.ensureInfraApplication(ctx, sc, comps)
	if err != nil {
		setCondition(status, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonInfraRenderFailed, err.Error())
		markConnectionUnobserved(status, reasonInfraRenderFailed, err.Error())
		return r.finish(ctx, sc, status, 0, err)
	}

	healthy, summary := summarizeInfraHealth(app, comps)
	status.Provisioning = &v1beta1.ProvisioningStatus{
		ApplicationName: app.Name,
		Phase:           string(app.Status.Phase),
		Healthy:         healthy,
		Message:         summary,
	}
	if !healthy {
		// An Application still working through its workflow is the normal in-flight
		// state. One whose workflow failed or terminated, or that applied everything and
		// still reports unhealthy, will not recover on its own, so it gets a reason an
		// operator can alert on without parsing the message.
		reason, connMsg := reasonProvisioning, "cluster is still being provisioned"
		if infraAppFailed(app.Status.Phase) {
			reason, connMsg = reasonInfraUnhealthy, "infrastructure Application is unhealthy"
		}
		setCondition(status, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reason, summary)
		markConnectionUnobserved(status, reason, connMsg)
		return r.finish(ctx, sc, status, provisionRequeue, nil)
	}
	setCondition(status, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionTrue, reasonInfraReady, summary)

	// Hand over. reconcileConnect starts from sc.Status, so persist what this pass learned
	// first; otherwise a connect pass whose own status does not change would drop the
	// provisioning projection on the floor.
	if statusNeedsWrite(sc.Status, *status) {
		if err := r.updateStatus(ctx, sc, status); err != nil {
			return ctrl.Result{}, err
		}
		sc.Status = *status
	}
	return r.reconcileConnect(ctx, sc)
}

// resolveInfraComponents turns the blueprint and its planes into Application components.
// Component names are prefixed with the plane name so two planes may both have a
// component called "cluster". Plane-level dependsOn and revision pins are refused rather
// than ignored: ClusterPlane has no revisions yet and plane ordering is a later slice.
func (r *Reconciler) resolveInfraComponents(ctx context.Context, sc *v1beta1.SpokeCluster) ([]common.ApplicationComponent, error) {
	ip := sc.Spec.InfraProvisioning
	if ip == nil || ip.BlueprintRef == nil || ip.BlueprintRef.Name == "" {
		return nil, fmt.Errorf("spec.infraProvisioning.blueprintRef.name is required in mode provision")
	}
	bp := &v1beta1.ClusterBlueprint{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: sc.Namespace, Name: ip.BlueprintRef.Name}, bp); err != nil {
		return nil, fmt.Errorf("reading ClusterBlueprint %s/%s: %w", sc.Namespace, ip.BlueprintRef.Name, err)
	}

	var comps []common.ApplicationComponent
	for _, p := range bp.Spec.Planes {
		if len(p.DependsOn) > 0 {
			return nil, fmt.Errorf("plane %q sets dependsOn; plane-level ordering is not supported for infraProvisioning yet", p.Name)
		}
		if p.Ref.Revision != "" {
			return nil, fmt.Errorf("plane %q pins ref.revision %q; ClusterPlane has no revisions yet", p.Name, p.Ref.Revision)
		}
		plane := &v1beta1.ClusterPlane{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: sc.Namespace, Name: p.Ref.Name}, plane); err != nil {
			return nil, fmt.Errorf("reading ClusterPlane %s/%s for plane %q: %w", sc.Namespace, p.Ref.Name, p.Name, err)
		}
		for _, c := range plane.Spec.Components {
			comp := common.ApplicationComponent{
				Name:       p.Name + "-" + c.Name,
				Type:       c.Type,
				Properties: c.Properties.DeepCopy(),
			}
			for _, d := range c.DependsOn {
				comp.DependsOn = append(comp.DependsOn, p.Name+"-"+d)
			}
			comps = append(comps, comp)
		}
	}
	if len(comps) == 0 {
		return nil, fmt.Errorf("ClusterBlueprint %s resolves to no components", bp.Name)
	}
	return comps, nil
}

// ensureInfraApplication creates or updates the owner-referenced infra Application. The
// owner reference is what deletes the Application when the SpokeCluster goes; the
// garbage-collect policy inside it is what decides whether the cluster survives that.
func (r *Reconciler) ensureInfraApplication(ctx context.Context, sc *v1beta1.SpokeCluster, comps []common.ApplicationComponent) (*v1beta1.Application, error) {
	policies, err := infraPolicies(sc, comps)
	if err != nil {
		return nil, err
	}
	app := &v1beta1.Application{ObjectMeta: metav1.ObjectMeta{Name: infraAppName(sc), Namespace: sc.Namespace}}
	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, app, func() error {
		if app.Labels == nil {
			app.Labels = map[string]string{}
		}
		app.Labels[labelSpokeName] = sc.Name
		app.Labels[labelSpokeRole] = roleInfra
		app.Spec.Components = comps
		app.Spec.Policies = policies
		return controllerutil.SetControllerReference(sc, app, r.Scheme)
	})
	if err != nil {
		return nil, fmt.Errorf("applying infra Application %s/%s: %w", sc.Namespace, infraAppName(sc), err)
	}
	if op != controllerutil.OperationResultNone {
		klog.InfoS("Infra Application reconciled", "spokecluster", klog.KObj(sc), "application", app.Name, "op", op)
	}
	return app, nil
}

// infraPolicies is always apply-once (substrates write defaults back into their own
// spec, and vela-core must not fight them) and take-over (a retained cluster's substrate
// objects lose their application labels when the owning SpokeCluster is deleted, and
// vela-core's pre-dispatch dry run refuses to adopt unmanaged objects unless told to; the
// policy only ever claims objects that belong to no application) plus, unless
// infraDeletionPolicy is delete, a garbage-collect rule that never recycles the substrate
// objects.
func infraPolicies(sc *v1beta1.SpokeCluster, comps []common.ApplicationComponent) ([]v1beta1.AppPolicy, error) {
	names := make([]string, 0, len(comps))
	for _, c := range comps {
		names = append(names, c.Name)
	}
	applyOnce, err := json.Marshal(map[string]any{"enable": true})
	if err != nil {
		return nil, err
	}
	takeOver, err := json.Marshal(map[string]any{
		"rules": []map[string]any{{
			"selector": map[string]any{"componentNames": names},
		}},
	})
	if err != nil {
		return nil, err
	}
	policies := []v1beta1.AppPolicy{
		{Name: "apply-once", Type: "apply-once", Properties: &runtime.RawExtension{Raw: applyOnce}},
		{Name: "take-over-retained", Type: "take-over", Properties: &runtime.RawExtension{Raw: takeOver}},
	}
	if sc.Spec.InfraDeletionPolicy == v1beta1.InfraDeletionPolicyDelete {
		return policies, nil
	}
	gc, err := json.Marshal(map[string]any{
		"rules": []map[string]any{{
			"selector": map[string]any{"componentNames": names},
			"strategy": "never",
		}},
	})
	if err != nil {
		return nil, err
	}
	return append(policies, v1beta1.AppPolicy{
		Name: "retain-infra", Type: "garbage-collect", Properties: &runtime.RawExtension{Raw: gc},
	}), nil
}

// infraAppFailed reports whether the Application has reached a phase it will not leave
// without intervention, as opposed to one it is still working through.
func infraAppFailed(phase common.ApplicationPhase) bool {
	switch phase {
	case common.ApplicationWorkflowFailed, common.ApplicationWorkflowTerminated, common.ApplicationUnhealthy:
		return true
	}
	return false
}

// summarizeInfraHealth reports whether every expected component is healthy and the
// Application is running, with a one-line summary for the condition message. Services
// are matched by the component names this pass rendered, so a stale entry left behind
// by a component that was since removed from the plane cannot make the count add up.
func summarizeInfraHealth(app *v1beta1.Application, comps []common.ApplicationComponent) (bool, string) {
	expected := make(map[string]struct{}, len(comps))
	for _, c := range comps {
		expected[c.Name] = struct{}{}
	}
	healthy := 0
	var msgs []string
	for _, s := range app.Status.Services {
		if _, ok := expected[s.Name]; !ok {
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
	all := len(expected) > 0 && healthy == len(expected) && app.Status.Phase == common.ApplicationRunning
	return all, summary
}
