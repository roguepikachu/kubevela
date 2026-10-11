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
	"encoding/json"
	"slices"

	wfTypesv1alpha1 "github.com/kubevela/pkg/apis/oam/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// Labels stamped on every Application this controller renders. The name label is how the
// controller finds the Applications it owns; plane and phase are what an operator greps for.
const (
	// LabelClusterName is the Cluster the Application was rendered for.
	LabelClusterName = "cluster.core.oam.dev/name"
	// LabelClusterPlane is the blueprint plane the Application renders.
	LabelClusterPlane = "cluster.core.oam.dev/plane"
	// LabelClusterPhase is the lifecycle phase the plane runs in.
	LabelClusterPhase = "cluster.core.oam.dev/phase"
)

const (
	// applicationPrefix is prepended to the plane name to form the Application name. Plane
	// names are unique within a blueprint and one Cluster per spoke is the design, so the
	// Cluster name is not part of it.
	applicationPrefix = "cluster-"

	// deployStepName and deployStepType name the final step of every plane workflow: a
	// local deploy of every component, since no topology policy is named.
	deployStepName = "deploy"
	deployStepType = "deploy"

	// waitStepPrefix and waitStepType name the steps that hold a plane until the
	// Applications it depends on are healthy.
	waitStepPrefix = "wait-"
	waitStepType   = "depends-on-app"
)

// lifecyclePhases is the order the stages run in. Implicit dependencies follow it: every
// plane of a phase waits on every plane of the previous non-empty phase.
var lifecyclePhases = []v1beta1.LifecyclePhase{
	v1beta1.LifecyclePhaseClusterInit,
	v1beta1.LifecyclePhasePlaneProvisioning,
	v1beta1.LifecyclePhaseHealthValidation,
}

// orderedPlane is one plane in render order together with the planes whose Applications
// it waits for, explicit dependsOn first, then the previous phase's planes.
type orderedPlane struct {
	Plane v1beta1.BlueprintPlaneRevision
	Deps  []string
}

// phaseRef returns the phase's blueprint ref, or nil when the Cluster skips that phase.
func phaseRef(cl *v1beta1.Cluster, phase v1beta1.LifecyclePhase) *v1beta1.PhaseSpec {
	switch phase {
	case v1beta1.LifecyclePhaseClusterInit:
		return cl.Spec.ClusterInit
	case v1beta1.LifecyclePhasePlaneProvisioning:
		return cl.Spec.PlaneProvisioning
	case v1beta1.LifecyclePhaseHealthValidation:
		return cl.Spec.HealthValidation
	}
	return nil
}

// blueprintRefNames is the distinct set of ClusterBlueprintRevision names the Cluster's
// phase refs point at, in phase order. The hub names the same revision three times today,
// so this is usually one name.
func blueprintRefNames(cl *v1beta1.Cluster) []string {
	var names []string
	for _, phase := range lifecyclePhases {
		ref := phaseRef(cl, phase)
		if ref == nil || ref.BlueprintRef.Name == "" || slices.Contains(names, ref.BlueprintRef.Name) {
			continue
		}
		names = append(names, ref.BlueprintRef.Name)
	}
	return names
}

// orderPlanes walks the phases in lifecycle order and, for each phase the Cluster runs,
// takes the planes of that phase from the revision the phase's ref names. A revision
// missing from revs contributes nothing; the caller reports it. The result is the render
// order, and each plane carries its full dependency list.
func orderPlanes(cl *v1beta1.Cluster, revs map[string]*v1beta1.ClusterBlueprintRevision) []orderedPlane {
	var out []orderedPlane
	var previous []string
	for _, phase := range lifecyclePhases {
		ref := phaseRef(cl, phase)
		if ref == nil {
			continue
		}
		rev := revs[ref.BlueprintRef.Name]
		if rev == nil {
			continue
		}
		var current []string
		for _, plane := range rev.Spec.Planes {
			if plane.Phase != phase {
				continue
			}
			deps := slices.Clone(plane.DependsOn)
			for _, dep := range previous {
				if !slices.Contains(deps, dep) {
					deps = append(deps, dep)
				}
			}
			out = append(out, orderedPlane{Plane: plane, Deps: deps})
			current = append(current, plane.Name)
		}
		if len(current) > 0 {
			previous = current
		}
	}
	return out
}

// applicationName is the Application a plane renders into.
func applicationName(plane string) string {
	return applicationPrefix + plane
}

// planeApplication renders one plane into the Application the spoke's vela-core runs. It
// is pure: the result is the desired state, and the controller copies it onto whatever
// already exists. Components are lifted verbatim (names unprefixed, properties and
// dependsOn kept); no policy is set, so the deploy step targets the local cluster; the
// workflow waits on each dependency's Application before deploying.
func planeApplication(cl *v1beta1.Cluster, plane v1beta1.BlueprintPlaneRevision, planeRev *v1beta1.ClusterPlaneRevision, deps []string) *v1beta1.Application {
	comps := make([]common.ApplicationComponent, 0, len(planeRev.Spec.Components))
	for _, c := range planeRev.Spec.Components {
		comps = append(comps, common.ApplicationComponent{
			Name:       c.Name,
			Type:       c.Type,
			Properties: c.Properties.DeepCopy(),
			DependsOn:  slices.Clone(c.DependsOn),
		})
	}

	steps := make([]wfTypesv1alpha1.WorkflowStep, 0, len(deps)+1)
	for _, dep := range deps {
		steps = append(steps, wfTypesv1alpha1.WorkflowStep{WorkflowStepBase: wfTypesv1alpha1.WorkflowStepBase{
			Name: waitStepPrefix + dep,
			Type: waitStepType,
			Properties: rawJSON(map[string]string{
				"name":      applicationName(dep),
				"namespace": cl.Namespace,
			}),
		}})
	}
	steps = append(steps, wfTypesv1alpha1.WorkflowStep{WorkflowStepBase: wfTypesv1alpha1.WorkflowStepBase{
		Name:       deployStepName,
		Type:       deployStepType,
		Properties: rawJSON(map[string][]string{"policies": {}}),
	}})

	return &v1beta1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Name:      applicationName(plane.Name),
			Namespace: cl.Namespace,
			Labels: map[string]string{
				LabelClusterName:  cl.Name,
				LabelClusterPlane: plane.Name,
				LabelClusterPhase: string(plane.Phase),
			},
		},
		Spec: v1beta1.ApplicationSpec{
			Components: comps,
			Workflow:   &v1beta1.Workflow{Steps: steps},
		},
	}
}

// rawJSON marshals a fixed-shape value into step properties. Only maps of strings and
// string slices reach it, which always marshal, so the error branch is unreachable in
// practice and yields empty properties rather than a panic in a controller.
func rawJSON(v any) *runtime.RawExtension {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return &runtime.RawExtension{Raw: raw}
}
