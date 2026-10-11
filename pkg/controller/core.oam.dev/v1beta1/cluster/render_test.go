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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

var _ = Describe("render", func() {
	Describe("orderPlanes", func() {
		It("walks the phases in lifecycle order and derives implicit dependencies from the previous non-empty phase", func() {
			rev := blueprintRev("bp-1",
				bpPlane("smoke", v1beta1.LifecyclePhaseHealthValidation),
				bpPlane("storage", v1beta1.LifecyclePhasePlaneProvisioning),
				bpPlane("cni", v1beta1.LifecyclePhaseClusterInit),
				bpPlane("policy", v1beta1.LifecyclePhaseClusterInit, "cni"),
			)
			planes := orderPlanes(clusterWithRefs("bp-1"), map[string]*v1beta1.ClusterBlueprintRevision{"bp-1": rev})

			var names []string
			deps := map[string][]string{}
			for _, p := range planes {
				names = append(names, p.Plane.Name)
				deps[p.Plane.Name] = p.Deps
			}
			Expect(names).To(Equal([]string{"cni", "policy", "storage", "smoke"}))
			Expect(deps["cni"]).To(BeEmpty())
			Expect(deps["policy"]).To(Equal([]string{"cni"}))
			Expect(deps["storage"]).To(Equal([]string{"cni", "policy"}))
			Expect(deps["smoke"]).To(Equal([]string{"storage"}))
		})

		It("does not repeat a dependency that is both explicit and implicit", func() {
			rev := blueprintRev("bp-1",
				bpPlane("cni", v1beta1.LifecyclePhaseClusterInit),
				bpPlane("storage", v1beta1.LifecyclePhasePlaneProvisioning, "cni"),
			)
			planes := orderPlanes(clusterWithRefs("bp-1"), map[string]*v1beta1.ClusterBlueprintRevision{"bp-1": rev})
			Expect(planes).To(HaveLen(2))
			Expect(planes[1].Deps).To(Equal([]string{"cni"}))
		})

		It("only takes a phase's planes from the revision that phase names", func() {
			cl := clusterWithRefs("bp-1")
			cl.Spec.PlaneProvisioning = &v1beta1.PhaseSpec{BlueprintRef: v1beta1.BlueprintReference{Name: "bp-2"}}
			cl.Spec.HealthValidation = nil
			revs := map[string]*v1beta1.ClusterBlueprintRevision{
				"bp-1": blueprintRev("bp-1",
					bpPlane("cni", v1beta1.LifecyclePhaseClusterInit),
					bpPlane("old-storage", v1beta1.LifecyclePhasePlaneProvisioning),
				),
				"bp-2": blueprintRev("bp-2",
					bpPlane("other-cni", v1beta1.LifecyclePhaseClusterInit),
					bpPlane("storage", v1beta1.LifecyclePhasePlaneProvisioning),
					bpPlane("smoke", v1beta1.LifecyclePhaseHealthValidation),
				),
			}
			planes := orderPlanes(cl, revs)
			var names []string
			for _, p := range planes {
				names = append(names, p.Plane.Name)
			}
			Expect(names).To(Equal([]string{"cni", "storage"}))
		})
	})

	Describe("unresolvedDependencies", func() {
		It("names every explicit dependsOn that no rendered plane satisfies", func() {
			rev := blueprintRev("bp-1",
				bpPlane("cni", v1beta1.LifecyclePhaseClusterInit),
				bpPlane("policy", v1beta1.LifecyclePhaseClusterInit, "cni", "zzz"),
				bpPlane("storage", v1beta1.LifecyclePhasePlaneProvisioning, "nope"),
			)
			planes := orderPlanes(clusterWithRefs("bp-1"), map[string]*v1beta1.ClusterBlueprintRevision{"bp-1": rev})
			unresolved := unresolvedDependencies(planes)
			Expect(unresolved).To(Equal(map[string][]string{"policy": {"zzz"}, "storage": {"nope"}}))
		})

		It("is empty when every dependency is rendered", func() {
			rev := blueprintRev("bp-1",
				bpPlane("cni", v1beta1.LifecyclePhaseClusterInit),
				bpPlane("storage", v1beta1.LifecyclePhasePlaneProvisioning, "cni"),
			)
			planes := orderPlanes(clusterWithRefs("bp-1"), map[string]*v1beta1.ClusterBlueprintRevision{"bp-1": rev})
			Expect(unresolvedDependencies(planes)).To(BeEmpty())
		})
	})

	Describe("planeApplication", func() {
		It("renders the Application shape the spoke's vela-core expects", func() {
			cl := clusterWithRefs("bp-1")
			plane := bpPlane("storage", v1beta1.LifecyclePhasePlaneProvisioning)
			prev := planeRev("storage", v1beta1.ClusterPlaneComponent{
				Name:       "ebs",
				Type:       "helm",
				Properties: &runtime.RawExtension{Raw: []byte(`{"chart":"ebs"}`)},
				DependsOn:  []string{"iam"},
			})
			app := planeApplication(cl, plane, prev, []string{"cni", "policy"})

			Expect(app.Name).To(Equal("cluster-storage"))
			Expect(app.Namespace).To(Equal(testNS))
			Expect(app.Labels).To(Equal(map[string]string{
				LabelClusterName:  testCluster,
				LabelClusterPlane: "storage",
				LabelClusterPhase: "planeProvisioning",
			}))
			Expect(app.Spec.Components).To(HaveLen(1))
			Expect(app.Spec.Components[0].Name).To(Equal("ebs"))
			Expect(app.Spec.Components[0].DependsOn).To(Equal([]string{"iam"}))
			Expect(string(app.Spec.Components[0].Properties.Raw)).To(MatchJSON(`{"chart":"ebs"}`))
			Expect(app.Spec.Policies).To(BeEmpty())
			Expect(stepNames(app)).To(Equal([]string{"wait-cni", "wait-policy", "deploy"}))
			Expect(app.Spec.Workflow.Steps[0].Type).To(Equal("depends-on-app"))
			Expect(string(app.Spec.Workflow.Steps[0].Properties.Raw)).To(MatchJSON(`{"name":"cluster-cni","namespace":"vela-system"}`))
			Expect(app.Spec.Workflow.Steps[2].Type).To(Equal("deploy"))
			Expect(string(app.Spec.Workflow.Steps[2].Properties.Raw)).To(MatchJSON(`{"policies":[]}`))
		})

		It("does not alias the plane revision's component properties", func() {
			prev := planeRev("storage", v1beta1.ClusterPlaneComponent{
				Name: "ebs", Type: "helm", Properties: &runtime.RawExtension{Raw: []byte(`{"a":1}`)},
			})
			app := planeApplication(clusterWithRefs("bp-1"), bpPlane("storage", v1beta1.LifecyclePhasePlaneProvisioning), prev, nil)
			app.Spec.Components[0].Properties.Raw[2] = 'b'
			Expect(string(prev.Spec.Components[0].Properties.Raw)).To(Equal(`{"a":1}`))
		})
	})
})
