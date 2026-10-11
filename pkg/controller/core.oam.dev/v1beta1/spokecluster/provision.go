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
	"errors"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	spokeadmission "github.com/oam-dev/kubevela/pkg/webhook/core.oam.dev/v1beta1/spokecluster"
)

// Provision condition reasons. Stable strings, like the connect reasons.
const (
	reasonBlueprintUnresolved  = "BlueprintUnresolved"
	reasonInfraRenderFailed    = "InfraRenderFailed"
	reasonProvisioning         = "Provisioning"
	reasonInfraFailed          = "InfraFailed"
	reasonInfraUnmanagedExists = "InfraUnmanagedExists"
	reasonInfraReady           = "InfraReady"
)

// provisionRequeue is how often a provisioning spoke re-reads its Application. EKS takes
// ten to twenty minutes; the Owns watch wakes the loop sooner on status changes, so this
// is a backstop rather than the cadence.
const provisionRequeue = 30 * time.Second

// reconcileProvision renders the whole Application (infra plane, and the bootstrap plane
// and platform step when the spec asks for them), reads the infra plane's health back,
// and hands over to the connect sequence only once every substrate component is healthy.
// Before that the cluster does not exist, so a connect pass would spend an sts:AssumeRole
// and an eks:DescribeCluster to learn nothing. The later workflow steps wait on the
// Connected condition inside vela-core, so rendering them up front costs nothing.
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

	render, err := r.renderApplication(ctx, sc)
	if err != nil {
		setCondition(status, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonBlueprintUnresolved, err.Error())
		markConnectionUnobserved(status, reasonBlueprintUnresolved, err.Error())
		return r.finish(ctx, sc, status, provisionRequeue, nil)
	}
	spec, err := render.spec(sc)
	if err != nil {
		setCondition(status, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonInfraRenderFailed, err.Error())
		markConnectionUnobserved(status, reasonInfraRenderFailed, err.Error())
		return r.finish(ctx, sc, status, 0, err)
	}
	app, err := r.ensureApplication(ctx, sc, spec)
	if errors.Is(err, errApplicationTerminating) {
		setCondition(status, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonProvisioning, err.Error())
		markConnectionUnobserved(status, reasonProvisioning, err.Error())
		return r.finish(ctx, sc, status, provisionRequeue, nil)
	}
	if err != nil {
		setCondition(status, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonInfraRenderFailed, err.Error())
		markConnectionUnobserved(status, reasonInfraRenderFailed, err.Error())
		return r.finish(ctx, sc, status, 0, err)
	}

	healthy, summary := componentsHealthy(app, render.infraNames(), localCluster)
	status.Provisioning = &v1beta1.ProvisioningStatus{
		ApplicationName: app.Name,
		Phase:           string(app.Status.Phase),
		Healthy:         healthy,
		Message:         summary,
	}
	if !healthy {
		// A provision step still running is the normal in-flight state. One that failed
		// will not recover on its own, so it gets a reason an operator can alert on
		// without parsing the message.
		reason, connMsg := reasonProvisioning, "cluster is still being provisioned"
		if step := workflowStep(app, stepProvision); stepFailed(step) {
			reason, connMsg = reasonInfraFailed, "infrastructure provisioning failed"
			summary = stepMessage(summary, step)
		}
		// A cluster that already exists outside any application fails the dispatch dry
		// run unless take-over is on. Retrying cannot fix that; naming the mode that can
		// is the whole value of the distinct reason. Checked on any phase rather than only
		// after the step failed: the step message is set the moment the dry run refuses,
		// while the Application may sit in runningWorkflow for a while before vela-core
		// gives up on the step.
		if sc.Spec.Mode == v1beta1.SpokeClusterModeProvision && unmanagedObjectsExist(app) {
			reason, connMsg = reasonInfraUnmanagedExists, "substrate objects already exist and belong to no application"
			summary += "; the cluster's substrate objects already exist and belong to no application: use mode adopt to claim them"
		}
		status.Provisioning.Message = summary
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
	return r.reconcileConnect(ctx, sc, app, render)
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
		comps = append(comps, planeComponents(plane, p.Name+"-")...)
	}
	if len(comps) == 0 {
		return nil, fmt.Errorf("ClusterBlueprint %s resolves to no components", bp.Name)
	}
	return comps, nil
}

// planeComponents lifts a plane's components into Application components. prefix is
// put in front of every component name and every dependsOn entry, so plane-local names
// stay consistent with each other: the infra renderer passes "<plane>-" to keep two
// planes' components apart, the bootstrap renderer passes "" because it renders one
// plane and keeps the plane's own names.
func planeComponents(plane *v1beta1.ClusterPlane, prefix string) []common.ApplicationComponent {
	comps := make([]common.ApplicationComponent, 0, len(plane.Spec.Components))
	for _, c := range plane.Spec.Components {
		comp := common.ApplicationComponent{
			Name:       prefix + c.Name,
			Type:       c.Type,
			Properties: c.Properties.DeepCopy(),
		}
		for _, d := range c.DependsOn {
			comp.DependsOn = append(comp.DependsOn, prefix+d)
		}
		comps = append(comps, comp)
	}
	return comps
}

// unmanagedObjectsExist reports whether vela-core refused the dispatch because the
// substrate objects already exist and belong to no application. Claiming them is
// adoption's job, so provision mode names the fix instead of retrying forever. Sub-steps
// are checked too because a step group reports the failing apply in its children. While
// the POC keeps the adopt policy on in every mode this cannot trigger; it stays for when
// that shortcut goes.
func unmanagedObjectsExist(app *v1beta1.Application) bool {
	if app.Status.Workflow == nil {
		return false
	}
	const marker = "exists but not managed by any application"
	for _, step := range app.Status.Workflow.Steps {
		if strings.Contains(step.Message, marker) {
			return true
		}
		for _, sub := range step.SubStepsStatus {
			if strings.Contains(sub.Message, marker) {
				return true
			}
		}
	}
	return false
}
