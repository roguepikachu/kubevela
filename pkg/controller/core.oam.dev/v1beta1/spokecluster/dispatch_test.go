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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// interceptedHubReconciler is connectedReconciler with the hub client rebuilt behind
// funcs, so a spec can fault individual hub calls.
func interceptedHubReconciler(t GinkgoTInterface, sc *v1beta1.SpokeCluster, funcs interceptor.Funcs, extra ...client.Object) *Reconciler {
	t.Helper()
	r := connectedReconciler(t, sc, extra...)
	hub := fake.NewClientBuilder().
		WithScheme(r.Scheme).
		WithObjects(append([]client.Object{sc}, extra...)...).
		WithStatusSubresource(&v1beta1.SpokeCluster{}, &v1beta1.Application{}).
		Build()
	r.Client = interceptor.NewClient(hub, funcs)
	return r
}

func isRevision(obj client.Object) bool {
	switch obj.(type) {
	case *v1beta1.ClusterPlaneRevision, *v1beta1.ClusterBlueprintRevision:
		return true
	}
	return false
}

func componentDefinition(ns, name string) *v1beta1.ComponentDefinition {
	return &v1beta1.ComponentDefinition{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
}

// dispatchFixtures is the blueprint, its planes, and the ComponentDefinitions the infra
// plane needs, plus a same-namespace definition for a built-in type that must stay home.
func dispatchFixtures(ns string) []client.Object {
	objs := append([]client.Object{dispatchBlueprint(ns)}, dispatchPlanes(ns)...)
	return append(objs,
		componentDefinition(ns, "aws-vpc"),
		componentDefinition(ns, "aws-eks"),
		componentDefinition(ns, "helm-flux"),
		componentDefinition(ns, "k8s-objects"),
	)
}

// dispatchedSpoke is a spoke an earlier pass left Dispatched.
func dispatchedSpoke(name, revision string) *v1beta1.SpokeCluster {
	sc := dispatchSpoke(name)
	sc.Status.DispatchedRevision = revision
	setCondition(&sc.Status, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionTrue, reasonDispatched, "revision "+revision)
	return sc
}

func readDispatchApp(t GinkgoTInterface, r *Reconciler, sc *v1beta1.SpokeCluster) *v1beta1.Application {
	t.Helper()
	app := &v1beta1.Application{}
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: sc.Namespace, Name: dispatchAppName(sc)}, app); err != nil {
		t.Fatalf("dispatch application missing: %v", err)
	}
	return app
}

func dispatchAppExists(r *Reconciler, sc *v1beta1.SpokeCluster) bool {
	err := r.Get(context.Background(), client.ObjectKey{Namespace: sc.Namespace, Name: dispatchAppName(sc)}, &v1beta1.Application{})
	if apierrors.IsNotFound(err) {
		return false
	}
	Expect(err).NotTo(HaveOccurred())
	return true
}

// dispatchAppOwnedBy is a dispatch Application named for sc but controlled by owner, with
// all three bundle components reported healthy and running.
func dispatchAppOwnedBy(t GinkgoTInterface, sc, owner *v1beta1.SpokeCluster) *v1beta1.Application {
	t.Helper()
	app := &v1beta1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: dispatchAppName(sc), Namespace: sc.Namespace},
		Spec: v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{
			{Name: "revisions", Type: "k8s-objects"},
			{Name: "definitions", Type: "k8s-objects"},
			{Name: "cluster", Type: "k8s-objects", DependsOn: []string{"revisions", "definitions"}},
		}},
		Status: common.AppStatus{
			Phase: common.ApplicationRunning,
			Services: []common.ApplicationComponentStatus{
				{Name: "revisions", Healthy: true},
				{Name: "definitions", Healthy: true},
				{Name: "cluster", Healthy: true},
			},
		},
	}
	if err := controllerutil.SetControllerReference(owner, app, testScheme(t)); err != nil {
		t.Fatalf("failed to set controller reference: %v", err)
	}
	return app
}

// objectsOf decodes a k8s-objects component's properties into its object list.
func objectsOf(comp common.ApplicationComponent) []map[string]any {
	var props struct {
		Objects []map[string]any `json:"objects"`
	}
	Expect(json.Unmarshal(comp.Properties.Raw, &props)).To(Succeed())
	return props.Objects
}

func objectNames(objs []map[string]any) []string {
	names := make([]string, 0, len(objs))
	for _, o := range objs {
		names = append(names, o["metadata"].(map[string]any)["name"].(string))
	}
	return names
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

var _ = It("DispatchAppNameIsDeterministic", func() {
	sc := dispatchSpoke("cpspoke1")
	Expect(dispatchAppName(sc)).To(Equal("sc-cpspoke1-dispatch"))
	long := dispatchSpoke("a-very-long-spoke-name-that-pushes-the-application-name-past-the-limit")
	Expect(dispatchAppName(long)).To(HaveSuffix("-dispatch"), "truncation keeps the whole suffix")
	Expect(validation.IsDNS1123Label(dispatchAppName(long))).To(BeEmpty())
})

var _ = It("DispatchRendersBundleIntoGatewayTargetedApplication", func() {
	t := GinkgoT()
	sc := dispatchSpoke("cpspoke1")
	r := connectedReconciler(t, sc, dispatchFixtures(sc.Namespace)...)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	bpRevs := listBlueprintRevisions(r, sc.Namespace)
	Expect(bpRevs).To(HaveLen(1))
	planeRevs := listPlaneRevisions(r, sc.Namespace)
	Expect(planeRevs).To(HaveLen(2))

	app := readDispatchApp(t, r, sc)
	Expect(app.Name).To(Equal("sc-cpspoke1-dispatch"))
	Expect(app.OwnerReferences).To(HaveLen(1))
	Expect(*app.OwnerReferences[0].Controller).To(BeTrue())
	Expect(app.OwnerReferences[0].Kind).To(Equal("SpokeCluster"))
	Expect(app.Labels).To(HaveKeyWithValue(labelSpokeName, "cpspoke1"))
	Expect(app.Labels).To(HaveKeyWithValue(labelSpokeRole, roleDispatch))
	Expect(app.Labels).To(HaveKeyWithValue(labelSpokeUID, string(sc.UID)))

	Expect(app.Spec.Components).To(HaveLen(3))
	for _, c := range app.Spec.Components {
		Expect(c.Type).To(Equal("k8s-objects"))
	}

	By("shipping the blueprint revision and every plane revision, trimmed to spec", func() {
		revisions := app.Spec.Components[0]
		Expect(revisions.Name).To(Equal("revisions"))
		Expect(revisions.DependsOn).To(BeEmpty())
		objs := objectsOf(revisions)
		Expect(objs).To(HaveLen(3))
		Expect(objs[0]["kind"]).To(Equal("ClusterBlueprintRevision"))
		Expect(objs[0]["apiVersion"]).To(Equal("core.oam.dev/v1beta1"))
		Expect(objectNames(objs)).To(Equal([]string{bpRevs[0].Name, planeRevs[0].Name, planeRevs[1].Name}))
		for _, o := range objs {
			Expect(mapKeys(o)).To(ConsistOf("apiVersion", "kind", "metadata", "spec"))
			md := o["metadata"].(map[string]any)
			Expect(mapKeys(md)).To(ConsistOf("name", "namespace", "labels"))
			Expect(md["namespace"]).To(Equal(sc.Namespace))
		}
		Expect(objs[1]["kind"]).To(Equal("ClusterPlaneRevision"))
		Expect(objs[1]["metadata"].(map[string]any)["labels"]).To(HaveKeyWithValue(v1beta1.LabelPlaneName, cniPlaneName))
		spec := objs[1]["spec"].(map[string]any)
		Expect(spec["components"].([]any)[0].(map[string]any)["properties"]).To(Equal(map[string]any{"chart": "cilium"}))
	})

	By("shipping the custom ComponentDefinitions the planes use, never the built-ins", func() {
		definitions := app.Spec.Components[1]
		Expect(definitions.Name).To(Equal("definitions"))
		objs := objectsOf(definitions)
		Expect(objectNames(objs)).To(Equal([]string{"aws-eks", "aws-vpc", "helm-flux"}), "sorted, and k8s-objects stays home")
		Expect(objs[0]["kind"]).To(Equal("ComponentDefinition"))
		Expect(mapKeys(objs[0])).To(ConsistOf("apiVersion", "kind", "metadata", "spec"))
	})

	By("shipping the spoke Cluster pinned to the blueprint revision, after the rest", func() {
		cluster := app.Spec.Components[2]
		Expect(cluster.Name).To(Equal("cluster"))
		Expect(cluster.DependsOn).To(Equal([]string{"revisions", "definitions"}))
		objs := objectsOf(cluster)
		Expect(objs).To(HaveLen(1))
		Expect(objs[0]["kind"]).To(Equal("Cluster"))
		Expect(objs[0]["apiVersion"]).To(Equal("core.oam.dev/v1beta1"))
		Expect(objs[0]).NotTo(HaveKey("status"))
		md := objs[0]["metadata"].(map[string]any)
		Expect(md["name"]).To(Equal("cpspoke1"))
		Expect(md["namespace"]).To(Equal(sc.Namespace))
		ref := map[string]any{"blueprintRef": map[string]any{"name": bpRevs[0].Name}}
		Expect(objs[0]["spec"]).To(Equal(map[string]any{"clusterInit": ref, "planeProvisioning": ref, "healthValidation": ref}))
	})

	Expect(app.Spec.Policies).To(HaveLen(2))
	Expect(app.Spec.Policies[0].Type).To(Equal("topology"))
	Expect(string(app.Spec.Policies[0].Properties.Raw)).To(MatchJSON(`{"clusters":["cpspoke1"]}`))
	Expect(app.Spec.Policies[1].Type).To(Equal("garbage-collect"))
	Expect(string(app.Spec.Policies[1].Properties.Raw)).To(MatchJSON(`{"rules":[{"selector":{"componentNames":["revisions","definitions","cluster"]},"strategy":"never"}]}`))

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionTrue, reasonProbeSucceeded)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, reasonDispatching)
	Expect(latest.Status.DispatchedRevision).To(Equal(bpRevs[0].Name))
})

var _ = It("DispatchOmitsDefinitionsWhenNoneAreCustom", func() {
	t := GinkgoT()
	sc := dispatchSpoke("cpspoke1")
	r := connectedReconciler(t, sc, append([]client.Object{dispatchBlueprint(sc.Namespace)}, dispatchPlanes(sc.Namespace)...)...)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	app := readDispatchApp(t, r, sc)
	Expect(app.Spec.Components).To(HaveLen(2))
	Expect(app.Spec.Components[0].Name).To(Equal("revisions"))
	Expect(app.Spec.Components[1].Name).To(Equal("cluster"))
	Expect(app.Spec.Components[1].DependsOn).To(Equal([]string{"revisions"}))
	Expect(string(app.Spec.Policies[1].Properties.Raw)).To(MatchJSON(`{"rules":[{"selector":{"componentNames":["revisions","cluster"]},"strategy":"never"}]}`))
})

type dispatchHealthCase struct {
	phase         common.ApplicationPhase
	clusterHealth bool
	wantStatus    metav1.ConditionStatus
	wantReason    string
}

var _ = DescribeTable("DispatchProjectsApplicationHealth", func(tc dispatchHealthCase) {
	t := GinkgoT()
	sc := dispatchSpoke("cpspoke1")
	app := dispatchAppOwnedBy(t, sc, sc)
	app.Status.Phase = tc.phase
	app.Status.Services[2].Healthy = tc.clusterHealth
	r := connectedReconciler(t, sc, append(dispatchFixtures(sc.Namespace), app)...)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionDispatched, tc.wantStatus, tc.wantReason)
	cond := meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionDispatched)
	Expect(cond.Message).To(ContainSubstring(latest.Status.DispatchedRevision), "the condition names the revision it reports on")
	Expect(cond.Message).To(ContainSubstring("components healthy"))
},
	Entry("all services healthy and running is Dispatched",
		dispatchHealthCase{phase: common.ApplicationRunning, clusterHealth: true, wantStatus: metav1.ConditionTrue, wantReason: reasonDispatched}),
	Entry("one unhealthy service is still Dispatching",
		dispatchHealthCase{phase: common.ApplicationRunning, clusterHealth: false, wantStatus: metav1.ConditionFalse, wantReason: reasonDispatching}),
	Entry("a failed workflow is DispatchFailed",
		dispatchHealthCase{phase: common.ApplicationWorkflowFailed, clusterHealth: false, wantStatus: metav1.ConditionFalse, wantReason: reasonDispatchFailed}),
)

var _ = It("DispatchUnresolvedBlueprintNeverStopsProbing", func() {
	t := GinkgoT()
	sc := dispatchedSpoke("cpspoke1", "platform-deadbeef")
	r := connectedReconciler(t, sc, dispatchAppOwnedBy(t, sc, sc))

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred(), "dispatch problems are state to report, not a controller fault")

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionTrue, reasonProbeSucceeded)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, reasonBlueprintUnresolved)
	cond := meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionDispatched)
	Expect(cond.Message).To(ContainSubstring("ClusterBlueprint " + sc.Namespace + "/" + dispatchBlueprintName))
	Expect(latest.Status.DispatchedRevision).To(Equal("platform-deadbeef"), "the last shipped revision stays on record")
	Expect(dispatchAppExists(r, sc)).To(BeTrue(), "an unresolved blueprint does not touch the bundle already shipped")
})

var _ = It("DispatchWaitsForBootstrap", func() {
	t := GinkgoT()
	sc := bootstrapSpoke("cpspoke1")
	sc.Spec.BlueprintRef = &v1beta1.BlueprintReference{Name: dispatchBlueprintName}
	r := connectedReconciler(t, sc, append(dispatchFixtures(sc.Namespace), bootstrapPlane(sc.Namespace))...)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapping)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, reasonWaitingForBootstrap)
	Expect(latest.Status.DispatchedRevision).To(BeEmpty())
	Expect(dispatchAppExists(r, sc)).To(BeFalse(), "nothing is shipped to a spoke whose runtime is not up")
	Expect(listBlueprintRevisions(r, sc.Namespace)).To(BeEmpty(), "and nothing is snapshotted yet either")

	By("dispatching once the runtime is Bootstrapped", func() {
		ready := bootstrappedSpoke("cpspoke1")
		ready.Spec.BlueprintRef = &v1beta1.BlueprintReference{Name: dispatchBlueprintName}
		r2 := connectedReconciler(t, ready, append(dispatchFixtures(ready.Namespace), bootstrapPlane(ready.Namespace), ownedBootstrapApp(t, ready))...)
		_, err := reconcileOnce(t, r2, ready)
		Expect(err).NotTo(HaveOccurred())
		latest := readSpoke(t, r2, ready)
		wantCondition(t, latest, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionTrue, reasonBootstrapped)
		wantCondition(t, latest, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, reasonDispatching)
		Expect(dispatchAppExists(r2, ready)).To(BeTrue())
	})
})

var _ = It("DispatchRemovedFromSpecDeletesApplication", func() {
	t := GinkgoT()
	sc := dispatchedSpoke("cpspoke1", "platform-deadbeef")
	app := dispatchAppOwnedBy(t, sc, sc)
	sc.Spec.BlueprintRef = nil
	r := connectedReconciler(t, sc, append(dispatchFixtures(sc.Namespace), app)...)
	rec := &recordingRecorder{}
	r.record = rec

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	Expect(dispatchAppExists(r, sc)).To(BeFalse(), "the Application goes with the spec field")
	latest := readSpoke(t, r, sc)
	Expect(meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionDispatched)).To(BeNil())
	Expect(latest.Status.DispatchedRevision).To(BeEmpty())
	wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionTrue, reasonProbeSucceeded)
	Expect(eventReasons(rec)).To(ContainElement(reasonDispatchReleased))
	Expect(listBlueprintRevisions(r, sc.Namespace)).To(BeEmpty(), "forgetting snapshots nothing")
})

// Forgetting must not wait for a probe, for the same reason as the bootstrap forget.
var _ = It("DispatchRemovedFromSpecIsForgottenWhileDisconnected", func() {
	t := GinkgoT()
	sc := dispatchedSpoke("cpspoke1", "platform-deadbeef")
	app := dispatchAppOwnedBy(t, sc, sc)
	sc.Spec.BlueprintRef = nil
	r := connectedReconciler(t, sc, app)
	r.probeFn = failingProbe

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionFalse, reasonProbeFailed)
	Expect(dispatchAppExists(r, sc)).To(BeFalse())
	Expect(meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionDispatched)).To(BeNil())
	Expect(latest.Status.DispatchedRevision).To(BeEmpty())
})

var _ = It("DispatchForgetLeavesForeignApplicationAlone", func() {
	t := GinkgoT()
	sc := dispatchedSpoke("cpspoke1", "platform-deadbeef")
	other := spokeIn("other", sc.Namespace, v1beta1.SpokeDeletionPolicyDetach)
	app := dispatchAppOwnedBy(t, sc, other)
	sc.Spec.BlueprintRef = nil
	r := connectedReconciler(t, sc, app)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	Expect(dispatchAppExists(r, sc)).To(BeTrue(), "an Application another SpokeCluster controls is not ours to delete")
	latest := readSpoke(t, r, sc)
	Expect(meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionDispatched)).To(BeNil())
	Expect(latest.Status.DispatchedRevision).To(BeEmpty())
})

// Two hub replicas, or a restart between Create and the status write, can both race a
// revision to the same name. The loser sees AlreadyExists while its cached client may
// still say NotFound; the content under that name is identical by construction, so the
// pass must carry on with its own candidate rather than report a failure.
var _ = It("DispatchToleratesConcurrentRevisionCreation", func() {
	resetSpokeMetrics()
	t := GinkgoT()
	sc := dispatchSpoke("cpspoke1")
	r := interceptedHubReconciler(t, sc, interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if isRevision(obj) {
				return apierrors.NewNotFound(schema.GroupResource{Group: "core.oam.dev", Resource: "revisions"}, key.Name)
			}
			return c.Get(ctx, key, obj, opts...)
		},
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if err := c.Create(ctx, obj, opts...); err != nil || !isRevision(obj) {
				return err
			}
			return apierrors.NewAlreadyExists(schema.GroupResource{Group: "core.oam.dev", Resource: "revisions"}, obj.GetName())
		},
	}, dispatchFixtures(sc.Namespace)...)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionDispatched, metav1.ConditionFalse, reasonDispatching)
	Expect(latest.Status.DispatchedRevision).To(MatchRegexp(`^platform-[0-9a-f]{8}$`))
	Expect(dispatchAppExists(r, sc)).To(BeTrue())
	Expect(testutil.CollectAndCount(spokeConditionFailures)).To(BeZero(), "a lost race is not a condition failure")
})

// A crash between CreateOrUpdate and the status write leaves an owned Application with
// no record of it in status. Forgetting must not trust status to know whether there is
// anything to delete.
var _ = DescribeTable("ForgetDeletesOrphanedOwnedApplication", func(orphan func(t GinkgoTInterface, sc *v1beta1.SpokeCluster) *v1beta1.Application, released string) {
	t := GinkgoT()
	sc := connectableSpoke("cpspoke1")
	app := orphan(t, sc)
	r := connectedReconciler(t, sc, app)
	rec := &recordingRecorder{}
	r.record = rec

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	err = r.Get(context.Background(), client.ObjectKeyFromObject(app), &v1beta1.Application{})
	Expect(apierrors.IsNotFound(err)).To(BeTrue(), "the orphan is collected on the first pass")
	Expect(eventReasons(rec)).To(ContainElement(released))
},
	Entry("dispatch", func(t GinkgoTInterface, sc *v1beta1.SpokeCluster) *v1beta1.Application {
		return dispatchAppOwnedBy(t, sc, sc)
	}, reasonDispatchReleased),
	Entry("bootstrap", func(t GinkgoTInterface, sc *v1beta1.SpokeCluster) *v1beta1.Application {
		return ownedBootstrapApp(t, sc)
	}, reasonBootstrapReleased),
)
