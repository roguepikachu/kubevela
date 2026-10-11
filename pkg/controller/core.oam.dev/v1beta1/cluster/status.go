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

package cluster

import (
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// Values of status.health.status.
const (
	// HealthStatusHealthy means every plane's Application is running with every
	// component healthy.
	HealthStatusHealthy = "Healthy"
	// HealthStatusDegraded means at least one plane is not healthy, whether it is still
	// coming up or its workflow failed.
	HealthStatusDegraded = "Degraded"
)

// Condition reasons for BlueprintResolved. They are stable strings an operator can match on.
const (
	reasonResolved              = "Resolved"
	reasonBlueprintMissing      = "BlueprintRevisionMissing"
	reasonPlaneRevisionMissing  = "PlaneRevisionMissing"
	messageApplicationNotFound  = "Application not found"
	messagePlaneRevisionMissing = "waiting for ClusterPlaneRevision "
)

// phaseForLifecycle maps the first lifecycle stage with an unhealthy plane to the Cluster
// phase that says that stage is still running.
var phaseForLifecycle = map[v1beta1.LifecyclePhase]v1beta1.ClusterPhase{
	v1beta1.LifecyclePhaseClusterInit:       v1beta1.ClusterPhaseInitializing,
	v1beta1.LifecyclePhasePlaneProvisioning: v1beta1.ClusterPhaseProvisioning,
	v1beta1.LifecyclePhaseHealthValidation:  v1beta1.ClusterPhaseValidating,
}

// observePlanes fills status.planes, status.health and status.phase from the planes in
// render order and the Applications found for them (keyed by plane name, absent when
// none exists yet). now is stamped as the health check time.
func observePlanes(status *v1beta1.ClusterStatus, planes []orderedPlane, apps map[string]*v1beta1.Application, now metav1.Time) {
	status.Planes = make([]v1beta1.PlaneStatus, 0, len(planes))
	found, healthy, failed := 0, 0, false
	for _, p := range planes {
		app := apps[p.Plane.Name]
		ps := v1beta1.PlaneStatus{
			Name:        p.Plane.Name,
			Phase:       p.Plane.Phase,
			Application: applicationName(p.Plane.Name),
			Message:     messageApplicationNotFound,
		}
		if app != nil {
			found++
			ps.Healthy = applicationHealthy(app)
			ps.Message = applicationMessage(app)
			if app.Status.Phase == common.ApplicationWorkflowFailed {
				failed = true
			}
		}
		if ps.Healthy {
			healthy++
		}
		status.Planes = append(status.Planes, ps)
	}

	health := &v1beta1.ClusterHealth{
		Status:        HealthStatusDegraded,
		PlanesHealthy: healthy,
		PlanesTotal:   len(planes),
		LastCheckTime: &now,
	}
	if len(planes) > 0 && healthy == len(planes) {
		health.Status = HealthStatusHealthy
	}
	status.Health = health
	status.Phase = clusterPhase(status.Planes, found, failed)
}

// clusterPhase is Pending until any Application exists, Degraded as soon as any workflow
// failed, otherwise the stage of the first unhealthy plane in lifecycle order, and Ready
// when there is none.
func clusterPhase(planes []v1beta1.PlaneStatus, found int, failed bool) v1beta1.ClusterPhase {
	if found == 0 {
		return v1beta1.ClusterPhasePending
	}
	if failed {
		return v1beta1.ClusterPhaseDegraded
	}
	for _, phase := range lifecyclePhases {
		for _, p := range planes {
			if p.Phase == phase && !p.Healthy {
				return phaseForLifecycle[phase]
			}
		}
	}
	return v1beta1.ClusterPhaseReady
}

// applicationHealthy is true when the Application finished its workflow and every
// component it reports is healthy. A freshly created Application has no phase yet and so
// is not healthy.
func applicationHealthy(app *v1beta1.Application) bool {
	if app.Status.Phase != common.ApplicationRunning {
		return false
	}
	for _, svc := range app.Status.Services {
		if !svc.Healthy {
			return false
		}
	}
	return true
}

// applicationMessage summarizes what the Application reports: its phase when it is not
// running, the workflow message when there is one, and each component's own message.
func applicationMessage(app *v1beta1.Application) string {
	var parts []string
	if app.Status.Phase != "" && app.Status.Phase != common.ApplicationRunning {
		parts = append(parts, "application "+string(app.Status.Phase))
	}
	if app.Status.Workflow != nil && app.Status.Workflow.Message != "" {
		parts = append(parts, app.Status.Workflow.Message)
	}
	for _, svc := range app.Status.Services {
		if svc.Message != "" {
			parts = append(parts, svc.Name+": "+svc.Message)
		}
	}
	return strings.Join(parts, "; ")
}

// setCondition records one condition. meta.SetStatusCondition leaves LastTransitionTime
// alone when the status has not flipped, so a steady Cluster does not churn its
// conditions on every pass.
func setCondition(status *v1beta1.ClusterStatus, condType string, condStatus metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(&status.Conditions, metav1.Condition{
		Type:    condType,
		Status:  condStatus,
		Reason:  reason,
		Message: message,
	})
}
