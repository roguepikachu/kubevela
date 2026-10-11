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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

const (
	dispatchBlueprintName = "platform"
	cniPlaneName          = "cni-plane"
	infraPlaneName        = "infra-plane"
)

// dispatchSpoke is a connectable spoke that also asks for a blueprint to be dispatched.
func dispatchSpoke(name string) *v1beta1.SpokeCluster {
	sc := connectableSpoke(name)
	sc.Spec.BlueprintRef = &v1beta1.BlueprintReference{Name: dispatchBlueprintName}
	return sc
}

// dispatchBlueprint composes two planes: the CNI plane in clusterInit and the infra
// plane with no phase (so it defaults to planeProvisioning) depending on it.
func dispatchBlueprint(ns string) *v1beta1.ClusterBlueprint {
	return &v1beta1.ClusterBlueprint{
		ObjectMeta: metav1.ObjectMeta{Name: dispatchBlueprintName, Namespace: ns},
		Spec: v1beta1.ClusterBlueprintSpec{
			Description: "platform blueprint",
			Planes: []v1beta1.BlueprintPlane{
				{Name: "cni", Ref: v1beta1.BlueprintReference{Name: cniPlaneName}, Phase: v1beta1.LifecyclePhaseClusterInit},
				{Name: "infra", Ref: v1beta1.BlueprintReference{Name: infraPlaneName}, DependsOn: []string{"cni"}},
			},
		},
	}
}

// dispatchPlanes are the two planes the blueprint composes. The CNI plane uses only a
// type the spoke resolves itself; the infra plane uses two custom types.
func dispatchPlanes(ns string) []client.Object {
	return []client.Object{
		&v1beta1.ClusterPlane{
			ObjectMeta: metav1.ObjectMeta{Name: cniPlaneName, Namespace: ns},
			Spec: v1beta1.ClusterPlaneSpec{Description: "cluster networking", Components: []v1beta1.ClusterPlaneComponent{
				{Name: "cilium", Type: "helm-flux", Properties: &runtime.RawExtension{Raw: []byte(`{"chart":"cilium"}`)}},
			}},
		},
		&v1beta1.ClusterPlane{
			ObjectMeta: metav1.ObjectMeta{Name: infraPlaneName, Namespace: ns},
			Spec: v1beta1.ClusterPlaneSpec{Components: []v1beta1.ClusterPlaneComponent{
				{Name: "vpc", Type: "aws-vpc", Properties: &runtime.RawExtension{Raw: []byte(`{"cidr":"10.0.0.0/16"}`)}},
				{Name: "eks", Type: "aws-eks", Properties: &runtime.RawExtension{Raw: []byte(`{"version":"1.31"}`)}, DependsOn: []string{"vpc"}},
			}},
		},
	}
}

func listPlaneRevisions(r *Reconciler, ns string) []v1beta1.ClusterPlaneRevision {
	list := &v1beta1.ClusterPlaneRevisionList{}
	Expect(r.List(context.Background(), list, client.InNamespace(ns))).To(Succeed())
	return list.Items
}

func listBlueprintRevisions(r *Reconciler, ns string) []v1beta1.ClusterBlueprintRevision {
	list := &v1beta1.ClusterBlueprintRevisionList{}
	Expect(r.List(context.Background(), list, client.InNamespace(ns))).To(Succeed())
	return list.Items
}

func readPlane(r *Reconciler, ns, name string) *v1beta1.ClusterPlane {
	plane := &v1beta1.ClusterPlane{}
	Expect(r.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, plane)).To(Succeed())
	return plane
}

var _ = It("EnsureRevisionsSnapshotsBlueprintAndPlanes", func() {
	t := GinkgoT()
	sc := dispatchSpoke("cpspoke1")
	r := newTestReconciler(t, append([]client.Object{sc, dispatchBlueprint(sc.Namespace)}, dispatchPlanes(sc.Namespace)...)...)

	bundle, err := r.ensureRevisions(context.Background(), sc)
	Expect(err).NotTo(HaveOccurred())
	Expect(bundle.planes).To(HaveLen(2))

	By("creating one immutable revision per plane, owned by its plane", func() {
		cni := bundle.planes[0]
		Expect(cni.Name).To(MatchRegexp(`^cni-plane-[0-9a-f]{8}$`))
		Expect(cni.Spec.PlaneName).To(Equal(cniPlaneName))
		Expect(cni.Spec.Hash).To(HaveLen(8))
		Expect(cni.Name).To(HaveSuffix(cni.Spec.Hash))
		Expect(cni.Spec.Description).To(Equal("cluster networking"))
		Expect(cni.Spec.Components).To(HaveLen(1))
		Expect(cni.Spec.Components[0].Name).To(Equal("cilium"))
		Expect(string(cni.Spec.Components[0].Properties.Raw)).To(MatchJSON(`{"chart":"cilium"}`))
		Expect(cni.Labels).To(HaveKeyWithValue(v1beta1.LabelPlaneName, cniPlaneName))
		Expect(cni.Labels).To(HaveKeyWithValue(v1beta1.LabelPlaneHash, cni.Spec.Hash))
		Expect(cni.OwnerReferences).To(HaveLen(1))
		Expect(cni.OwnerReferences[0].Kind).To(Equal("ClusterPlane"))
		Expect(cni.OwnerReferences[0].Name).To(Equal(cniPlaneName))

		infra := bundle.planes[1]
		Expect(infra.Name).To(MatchRegexp(`^infra-plane-[0-9a-f]{8}$`))
		Expect(infra.Spec.Components).To(HaveLen(2))
		Expect(infra.Spec.Components[1].DependsOn).To(Equal([]string{"vpc"}))
		Expect(listPlaneRevisions(r, sc.Namespace)).To(HaveLen(2))
	})

	By("creating the blueprint revision that pins those plane revisions", func() {
		bp := bundle.blueprint
		Expect(bp.Name).To(MatchRegexp(`^platform-[0-9a-f]{8}$`))
		Expect(bp.Spec.BlueprintName).To(Equal(dispatchBlueprintName))
		Expect(bp.Spec.Description).To(Equal("platform blueprint"))
		Expect(bp.Name).To(HaveSuffix(bp.Spec.Hash))
		Expect(bp.Spec.Planes).To(HaveLen(2))
		Expect(bp.Spec.Planes[0]).To(Equal(v1beta1.BlueprintPlaneRevision{
			Name: "cni", Phase: v1beta1.LifecyclePhaseClusterInit, PlaneRevision: bundle.planes[0].Name,
		}))
		Expect(bp.Spec.Planes[1]).To(Equal(v1beta1.BlueprintPlaneRevision{
			Name: "infra", Phase: v1beta1.LifecyclePhasePlaneProvisioning, PlaneRevision: bundle.planes[1].Name, DependsOn: []string{"cni"},
		}), "a plane without a phase is recorded as planeProvisioning")
		Expect(bp.Labels).To(HaveKeyWithValue(v1beta1.LabelBlueprintName, dispatchBlueprintName))
		Expect(bp.Labels).To(HaveKeyWithValue(v1beta1.LabelBlueprintHash, bp.Spec.Hash))
		Expect(bp.OwnerReferences).To(HaveLen(1))
		Expect(bp.OwnerReferences[0].Kind).To(Equal("ClusterBlueprint"))
		Expect(bp.OwnerReferences[0].Name).To(Equal(dispatchBlueprintName))
		Expect(listBlueprintRevisions(r, sc.Namespace)).To(HaveLen(1))
	})

	By("returning the same revisions for unchanged content", func() {
		again, err := r.ensureRevisions(context.Background(), sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(again.blueprint.Name).To(Equal(bundle.blueprint.Name))
		Expect(again.planes[0].Name).To(Equal(bundle.planes[0].Name))
		Expect(again.planes[1].Name).To(Equal(bundle.planes[1].Name))
		Expect(listPlaneRevisions(r, sc.Namespace)).To(HaveLen(2))
		Expect(listBlueprintRevisions(r, sc.Namespace)).To(HaveLen(1))
	})

	By("cutting new plane and blueprint revisions when a component changes", func() {
		plane := readPlane(r, sc.Namespace, infraPlaneName)
		plane.Spec.Components[1].Properties = &runtime.RawExtension{Raw: []byte(`{"version":"1.32"}`)}
		Expect(r.Update(context.Background(), plane)).To(Succeed())

		next, err := r.ensureRevisions(context.Background(), sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(next.planes[0].Name).To(Equal(bundle.planes[0].Name), "the unchanged plane keeps its revision")
		Expect(next.planes[1].Name).NotTo(Equal(bundle.planes[1].Name))
		Expect(string(next.planes[1].Spec.Components[1].Properties.Raw)).To(MatchJSON(`{"version":"1.32"}`))
		Expect(next.blueprint.Name).NotTo(Equal(bundle.blueprint.Name))
		Expect(next.blueprint.Spec.Planes[1].PlaneRevision).To(Equal(next.planes[1].Name))
		Expect(listPlaneRevisions(r, sc.Namespace)).To(HaveLen(3))
		Expect(listBlueprintRevisions(r, sc.Namespace)).To(HaveLen(2))

		previous := &v1beta1.ClusterPlaneRevision{}
		Expect(r.Get(context.Background(), client.ObjectKey{Namespace: sc.Namespace, Name: bundle.planes[1].Name}, previous)).To(Succeed())
		Expect(string(previous.Spec.Components[1].Properties.Raw)).To(MatchJSON(`{"version":"1.31"}`), "an existing revision is never rewritten")
	})
})

var _ = It("EnsureRevisionsPinnedRevisionIsReadNotSnapshotted", func() {
	t := GinkgoT()
	sc := dispatchSpoke("cpspoke1")
	r := newTestReconciler(t, append([]client.Object{sc, dispatchBlueprint(sc.Namespace)}, dispatchPlanes(sc.Namespace)...)...)
	first, err := r.ensureRevisions(context.Background(), sc)
	Expect(err).NotTo(HaveOccurred())

	plane := readPlane(r, sc.Namespace, cniPlaneName)
	plane.Spec.Components[0].Properties = &runtime.RawExtension{Raw: []byte(`{"chart":"calico"}`)}
	Expect(r.Update(context.Background(), plane)).To(Succeed())
	sc.Spec.BlueprintRef.Revision = first.blueprint.Name

	pinned, err := r.ensureRevisions(context.Background(), sc)
	Expect(err).NotTo(HaveOccurred())
	Expect(pinned.blueprint.Name).To(Equal(first.blueprint.Name))
	Expect(pinned.planes[0].Name).To(Equal(first.planes[0].Name))
	Expect(string(pinned.planes[0].Spec.Components[0].Properties.Raw)).To(MatchJSON(`{"chart":"cilium"}`))
	Expect(listPlaneRevisions(r, sc.Namespace)).To(HaveLen(2), "a pin reads what exists and snapshots nothing")

	sc.Spec.BlueprintRef.Revision = "platform-00000000"
	_, err = r.ensureRevisions(context.Background(), sc)
	Expect(errors.Is(err, errBlueprintUnresolved)).To(BeTrue(), "got %v", err)
})

var _ = DescribeTable("EnsureRevisionsUnresolved", func(seed func(ns string) []client.Object, wantKind, wantName string) {
	t := GinkgoT()
	sc := dispatchSpoke("cpspoke1")
	r := newTestReconciler(t, append([]client.Object{sc}, seed(sc.Namespace)...)...)

	_, err := r.ensureRevisions(context.Background(), sc)
	Expect(errors.Is(err, errBlueprintUnresolved)).To(BeTrue(), "got %v", err)
	Expect(err.Error()).To(ContainSubstring(wantKind + " " + sc.Namespace + "/" + wantName))
	Expect(listPlaneRevisions(r, sc.Namespace)).To(BeEmpty())
	Expect(listBlueprintRevisions(r, sc.Namespace)).To(BeEmpty())
},
	Entry("a missing blueprint", func(ns string) []client.Object { return dispatchPlanes(ns) }, "ClusterBlueprint", dispatchBlueprintName),
	Entry("a missing plane", func(ns string) []client.Object { return []client.Object{dispatchBlueprint(ns)} }, "ClusterPlane", cniPlaneName),
)

var _ = It("HashSpecIsStableAndContentSensitive", func() {
	planes := dispatchPlanes("ns")
	a := planes[1].(*v1beta1.ClusterPlane).Spec
	b := planes[1].(*v1beta1.ClusterPlane).DeepCopy().Spec
	Expect(hashSpec(a)).To(HaveLen(8))
	Expect(hashSpec(a)).To(Equal(hashSpec(b)))
	b.Components[0].Properties = &runtime.RawExtension{Raw: []byte(`{"cidr":"10.1.0.0/16"}`)}
	Expect(hashSpec(a)).NotTo(Equal(hashSpec(b)))
})
