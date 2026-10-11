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
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	apitypes "k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

const (
	testNS      = "vela-system"
	testCluster = "spoke-a"
)

// testScheme carries the vela API group so SetControllerReference can resolve the
// Cluster GVK.
func testScheme(t GinkgoTInterface) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1beta1.AddToScheme(scheme); err != nil {
		t.Fatalf("failed to add v1beta1 to scheme: %v", err)
	}
	return scheme
}

// newTestReconciler builds a Reconciler over a fake client seeded with objs. Both the
// Cluster and Application status subresources are registered: the controller writes
// Cluster status through it, and the tests set Application status through it without the
// controller's CreateOrUpdate wiping it on the next pass.
func newTestReconciler(t GinkgoTInterface, objs ...client.Object) *Reconciler {
	t.Helper()
	scheme := testScheme(t)
	cli := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&v1beta1.Cluster{}, &v1beta1.Application{}).
		Build()
	return &Reconciler{Client: cli, Scheme: scheme}
}

// clusterWithRefs is a Cluster whose three phase refs all name one blueprint revision,
// which is how the hub dispatches a bundle today.
func clusterWithRefs(revName string) *v1beta1.Cluster {
	ref := &v1beta1.PhaseSpec{BlueprintRef: v1beta1.BlueprintReference{Name: revName}}
	return &v1beta1.Cluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      testCluster,
			Namespace: testNS,
			UID:       apitypes.UID("uid-" + testCluster),
		},
		Spec: v1beta1.ClusterSpec{ClusterInit: ref, PlaneProvisioning: ref, HealthValidation: ref},
	}
}

func blueprintRev(name string, planes ...v1beta1.BlueprintPlaneRevision) *v1beta1.ClusterBlueprintRevision {
	return &v1beta1.ClusterBlueprintRevision{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNS},
		Spec: v1beta1.ClusterBlueprintRevisionSpec{
			BlueprintName: "bp",
			Hash:          name,
			Planes:        planes,
		},
	}
}

// bpPlane pins plane name to the ClusterPlaneRevision "<name>-rev" in the given phase.
func bpPlane(name string, phase v1beta1.LifecyclePhase, dependsOn ...string) v1beta1.BlueprintPlaneRevision {
	return v1beta1.BlueprintPlaneRevision{Name: name, Phase: phase, PlaneRevision: name + "-rev", DependsOn: dependsOn}
}

func planeRev(planeName string, comps ...v1beta1.ClusterPlaneComponent) *v1beta1.ClusterPlaneRevision {
	if len(comps) == 0 {
		comps = []v1beta1.ClusterPlaneComponent{{
			Name:       "main",
			Type:       "k8s-objects",
			Properties: &runtime.RawExtension{Raw: []byte(`{"objects":[]}`)},
		}}
	}
	return &v1beta1.ClusterPlaneRevision{
		ObjectMeta: metav1.ObjectMeta{Name: planeName + "-rev", Namespace: testNS},
		Spec: v1beta1.ClusterPlaneRevisionSpec{
			PlaneName:  planeName,
			Hash:       planeName + "-hash",
			Components: comps,
		},
	}
}

func reconcile(r *Reconciler) (ctrl.Result, error) {
	return r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: apitypes.NamespacedName{Namespace: testNS, Name: testCluster},
	})
}

func getCluster(r *Reconciler) *v1beta1.Cluster {
	c := &v1beta1.Cluster{}
	ExpectWithOffset(1, r.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: testCluster}, c)).To(Succeed())
	return c
}

func getApp(r *Reconciler, name string) (*v1beta1.Application, error) {
	app := &v1beta1.Application{}
	err := r.Get(context.Background(), client.ObjectKey{Namespace: testNS, Name: name}, app)
	return app, err
}

func mustGetApp(r *Reconciler, name string) *v1beta1.Application {
	app, err := getApp(r, name)
	ExpectWithOffset(1, err).NotTo(HaveOccurred(), "Application %s", name)
	return app
}

func listApps(r *Reconciler) []v1beta1.Application {
	apps := &v1beta1.ApplicationList{}
	ExpectWithOffset(1, r.List(context.Background(), apps, client.InNamespace(testNS))).To(Succeed())
	return apps.Items
}

// setAppStatus writes the Application phase and marks every component healthy or not.
func setAppStatus(r *Reconciler, name string, phase common.ApplicationPhase, healthy bool) {
	app := mustGetApp(r, name)
	app.Status.Phase = phase
	app.Status.Services = nil
	for _, c := range app.Spec.Components {
		app.Status.Services = append(app.Status.Services, common.ApplicationComponentStatus{
			Name: c.Name, Healthy: healthy, Message: c.Name + " status",
		})
	}
	ExpectWithOffset(1, r.Status().Update(context.Background(), app)).To(Succeed())
}

func markHealthy(r *Reconciler, names ...string) {
	for _, n := range names {
		setAppStatus(r, n, common.ApplicationRunning, true)
	}
}

func stepNames(app *v1beta1.Application) []string {
	Expect(app.Spec.Workflow).NotTo(BeNil(), "Application %s has no workflow", app.Name)
	var names []string
	for _, s := range app.Spec.Workflow.Steps {
		names = append(names, s.Name)
	}
	return names
}

var _ = Describe("Cluster controller", func() {
	ctx := context.Background()

	Describe("rendering a blueprint revision", func() {
		var r *Reconciler

		BeforeEach(func() {
			r = newTestReconciler(GinkgoT(),
				clusterWithRefs("bp-1"),
				blueprintRev("bp-1",
					bpPlane("foundation-policy", v1beta1.LifecyclePhaseClusterInit),
					bpPlane("foundation-certs", v1beta1.LifecyclePhaseClusterInit),
				),
				planeRev("foundation-policy", v1beta1.ClusterPlaneComponent{
					Name:       "kyverno",
					Type:       "helm",
					Properties: &runtime.RawExtension{Raw: []byte(`{"chart":"kyverno"}`)},
				}, v1beta1.ClusterPlaneComponent{
					Name:      "policies",
					Type:      "k8s-objects",
					DependsOn: []string{"kyverno"},
				}),
				planeRev("foundation-certs"),
			)
			_, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
		})

		It("creates one Application per plane in the Cluster namespace, owned by the Cluster", func() {
			cl := getCluster(r)
			for _, name := range []string{"cluster-foundation-policy", "cluster-foundation-certs"} {
				app := mustGetApp(r, name)
				Expect(app.Namespace).To(Equal(testNS))
				Expect(metav1.IsControlledBy(app, cl)).To(BeTrue(), "%s should be controlled by the Cluster", name)
			}
			Expect(listApps(r)).To(HaveLen(2))
		})

		It("copies the plane components verbatim, with no policies", func() {
			app := mustGetApp(r, "cluster-foundation-policy")
			Expect(app.Spec.Components).To(HaveLen(2))
			Expect(app.Spec.Components[0].Name).To(Equal("kyverno"))
			Expect(app.Spec.Components[0].Type).To(Equal("helm"))
			Expect(app.Spec.Components[0].Properties).NotTo(BeNil())
			Expect(string(app.Spec.Components[0].Properties.Raw)).To(MatchJSON(`{"chart":"kyverno"}`))
			Expect(app.Spec.Components[1].Name).To(Equal("policies"))
			Expect(app.Spec.Components[1].DependsOn).To(Equal([]string{"kyverno"}))
			Expect(app.Spec.Policies).To(BeEmpty())
		})

		It("renders a single local deploy step for a plane with no dependencies", func() {
			app := mustGetApp(r, "cluster-foundation-policy")
			Expect(app.Spec.Workflow).NotTo(BeNil())
			Expect(app.Spec.Workflow.Steps).To(HaveLen(1))
			step := app.Spec.Workflow.Steps[0]
			Expect(step.Name).To(Equal("deploy"))
			Expect(step.Type).To(Equal("deploy"))
			Expect(step.Properties).NotTo(BeNil())
			Expect(string(step.Properties.Raw)).To(MatchJSON(`{"policies":[]}`))
		})

		It("labels each Application with the cluster, plane and phase", func() {
			app := mustGetApp(r, "cluster-foundation-certs")
			Expect(app.Labels).To(HaveKeyWithValue(LabelClusterName, testCluster))
			Expect(app.Labels).To(HaveKeyWithValue(LabelClusterPlane, "foundation-certs"))
			Expect(app.Labels).To(HaveKeyWithValue(LabelClusterPhase, "clusterInit"))
		})

		It("resolves the blueprint and reports Initializing while the clusterInit planes are not yet healthy", func() {
			cl := getCluster(r)
			cond := meta.FindStatusCondition(cl.Status.Conditions, v1beta1.ClusterConditionBlueprintResolved)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
			Expect(cl.Status.Phase).To(Equal(v1beta1.ClusterPhaseInitializing))
			Expect(cl.Status.Planes).To(HaveLen(2))
		})
	})

	Describe("ordering planes", func() {
		var r *Reconciler

		BeforeEach(func() {
			r = newTestReconciler(GinkgoT(),
				clusterWithRefs("bp-1"),
				blueprintRev("bp-1",
					bpPlane("cni", v1beta1.LifecyclePhaseClusterInit),
					bpPlane("policy", v1beta1.LifecyclePhaseClusterInit, "cni"),
					bpPlane("storage", v1beta1.LifecyclePhasePlaneProvisioning),
					bpPlane("network", v1beta1.LifecyclePhasePlaneProvisioning),
					bpPlane("smoke", v1beta1.LifecyclePhaseHealthValidation),
				),
				planeRev("cni"), planeRev("policy"), planeRev("storage"), planeRev("network"), planeRev("smoke"),
			)
			_, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
		})

		It("waits on an explicit dependsOn before deploying", func() {
			app := mustGetApp(r, "cluster-policy")
			Expect(stepNames(app)).To(Equal([]string{"wait-cni", "deploy"}))
			wait := app.Spec.Workflow.Steps[0]
			Expect(wait.Type).To(Equal("depends-on-app"))
			Expect(string(wait.Properties.Raw)).To(MatchJSON(`{"name":"cluster-cni","namespace":"vela-system"}`))
		})

		It("waits on every plane of the previous phase", func() {
			Expect(stepNames(mustGetApp(r, "cluster-storage"))).To(Equal([]string{"wait-cni", "wait-policy", "deploy"}))
			Expect(stepNames(mustGetApp(r, "cluster-network"))).To(Equal([]string{"wait-cni", "wait-policy", "deploy"}))
			Expect(stepNames(mustGetApp(r, "cluster-smoke"))).To(Equal([]string{"wait-storage", "wait-network", "deploy"}))
		})

		It("has the first plane deploy straight away", func() {
			Expect(stepNames(mustGetApp(r, "cluster-cni"))).To(Equal([]string{"deploy"}))
		})
	})

	Describe("ordering planes across an empty phase", func() {
		It("derives implicit dependencies from the previous non-empty phase", func() {
			r := newTestReconciler(GinkgoT(),
				clusterWithRefs("bp-2"),
				blueprintRev("bp-2",
					bpPlane("cni", v1beta1.LifecyclePhaseClusterInit),
					bpPlane("smoke", v1beta1.LifecyclePhaseHealthValidation),
				),
				planeRev("cni"), planeRev("smoke"),
			)
			_, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(stepNames(mustGetApp(r, "cluster-smoke"))).To(Equal([]string{"wait-cni", "deploy"}))
		})
	})

	Describe("an explicit dependsOn on a plane the blueprint does not render", func() {
		It("reports it through BlueprintResolved and renders every other plane", func() {
			r := newTestReconciler(GinkgoT(),
				clusterWithRefs("bp-1"),
				blueprintRev("bp-1",
					bpPlane("cni", v1beta1.LifecyclePhaseClusterInit),
					bpPlane("policy", v1beta1.LifecyclePhaseClusterInit, "zzz"),
				),
				planeRev("cni"), planeRev("policy"),
			)
			res, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(30 * time.Second))

			mustGetApp(r, "cluster-cni")
			_, err = getApp(r, "cluster-policy")
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "a plane waiting on an unknown plane must not be rendered, got %v", err)

			cl := getCluster(r)
			cond := meta.FindStatusCondition(cl.Status.Conditions, v1beta1.ClusterConditionBlueprintResolved)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Message).To(ContainSubstring("policy"))
			Expect(cond.Message).To(ContainSubstring("zzz"))
			Expect(cl.Status.Planes).To(HaveLen(2))
		})
	})

	Describe("status aggregation", func() {
		var r *Reconciler
		const (
			initApp  = "cluster-init"
			infraApp = "cluster-infra"
			checkApp = "cluster-check"
		)

		BeforeEach(func() {
			r = newTestReconciler(GinkgoT(),
				clusterWithRefs("bp-1"),
				blueprintRev("bp-1",
					bpPlane("init", v1beta1.LifecyclePhaseClusterInit),
					bpPlane("infra", v1beta1.LifecyclePhasePlaneProvisioning),
					bpPlane("check", v1beta1.LifecyclePhaseHealthValidation),
				),
				planeRev("init"), planeRev("infra"), planeRev("check"),
			)
			_, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
		})

		It("is Ready and Healthy once every Application is running and healthy", func() {
			markHealthy(r, initApp, infraApp, checkApp)
			res, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(5 * time.Minute))

			cl := getCluster(r)
			Expect(cl.Status.Phase).To(Equal(v1beta1.ClusterPhaseReady))
			Expect(cl.Status.Planes).To(HaveLen(3))
			Expect(cl.Status.Planes[0]).To(MatchFields(IgnoreExtras, Fields{
				"Name":          Equal("init"),
				"Phase":         Equal(v1beta1.LifecyclePhaseClusterInit),
				"Application":   Equal(initApp),
				"PlaneRevision": Equal("init-rev"),
				"Healthy":       BeTrue(),
				"Message":       ContainSubstring("main status"),
			}))
			Expect(cl.Status.Planes[1].Application).To(Equal(infraApp))
			Expect(cl.Status.Planes[2].Application).To(Equal(checkApp))
			Expect(cl.Status.Health).NotTo(BeNil())
			Expect(cl.Status.Health.Status).To(Equal(v1beta1.ClusterHealthStatusHealthy))
			Expect(cl.Status.Health.PlanesHealthy).To(Equal(3))
			Expect(cl.Status.Health.PlanesTotal).To(Equal(3))
			Expect(cl.Status.Health.LastCheckTime).NotTo(BeNil())
			cond := meta.FindStatusCondition(cl.Status.Conditions, v1beta1.ClusterConditionBlueprintResolved)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})

		It("is Initializing while a clusterInit plane is unhealthy", func() {
			markHealthy(r, infraApp, checkApp)
			setAppStatus(r, initApp, common.ApplicationRunningWorkflow, false)
			res, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(Equal(30 * time.Second))
			cl := getCluster(r)
			Expect(cl.Status.Phase).To(Equal(v1beta1.ClusterPhaseInitializing))
			Expect(cl.Status.Health.Status).To(Equal(v1beta1.ClusterHealthStatusProgressing))
			Expect(cl.Status.Health.PlanesHealthy).To(Equal(2))
		})

		It("is Provisioning while a planeProvisioning plane is unhealthy", func() {
			markHealthy(r, initApp, checkApp)
			setAppStatus(r, infraApp, common.ApplicationRunningWorkflow, false)
			_, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(getCluster(r).Status.Phase).To(Equal(v1beta1.ClusterPhaseProvisioning))
		})

		It("is Validating while a healthValidation plane is unhealthy", func() {
			markHealthy(r, initApp, infraApp)
			setAppStatus(r, checkApp, common.ApplicationRunningWorkflow, false)
			_, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(getCluster(r).Status.Phase).To(Equal(v1beta1.ClusterPhaseValidating))
		})

		It("is Degraded when any Application's workflow failed", func() {
			markHealthy(r, initApp, checkApp)
			setAppStatus(r, infraApp, common.ApplicationWorkflowFailed, false)
			_, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			cl := getCluster(r)
			Expect(cl.Status.Phase).To(Equal(v1beta1.ClusterPhaseDegraded))
			Expect(cl.Status.Health.Status).To(Equal(v1beta1.ClusterHealthStatusDegraded))
		})

		It("reports Progressing with 0/0 when the referenced phases render no planes", func() {
			r0 := newTestReconciler(GinkgoT(),
				&v1beta1.Cluster{
					ObjectMeta: metav1.ObjectMeta{Name: testCluster, Namespace: testNS, UID: apitypes.UID("uid-" + testCluster)},
					Spec: v1beta1.ClusterSpec{
						ClusterInit: &v1beta1.PhaseSpec{BlueprintRef: v1beta1.BlueprintReference{Name: "bp-1"}},
					},
				},
				blueprintRev("bp-1", bpPlane("infra", v1beta1.LifecyclePhasePlaneProvisioning)),
				planeRev("infra"),
			)
			_, err := reconcile(r0)
			Expect(err).NotTo(HaveOccurred())
			cl := getCluster(r0)
			Expect(cl.Status.Phase).To(Equal(v1beta1.ClusterPhasePending))
			Expect(cl.Status.Planes).To(BeEmpty())
			Expect(cl.Status.Health).NotTo(BeNil())
			Expect(cl.Status.Health.Status).To(Equal(v1beta1.ClusterHealthStatusProgressing))
			Expect(cl.Status.Health.PlanesHealthy).To(Equal(0))
			Expect(cl.Status.Health.PlanesTotal).To(Equal(0))
			Expect(listApps(r0)).To(BeEmpty())
		})

		It("reports every plane with its Application name on the very first pass", func() {
			// The first pass is the one where an informer cache would not yet hold the
			// Applications it just created, so planes[] has to come from the written objects.
			cl := getCluster(r)
			Expect(cl.Status.Planes).To(HaveLen(3))
			Expect(cl.Status.Planes[0].Application).To(Equal(initApp))
			Expect(cl.Status.Planes[1].Application).To(Equal(infraApp))
			Expect(cl.Status.Planes[2].Application).To(Equal(checkApp))
			Expect(cl.Status.Phase).To(Equal(v1beta1.ClusterPhaseInitializing))
		})

		It("does not rewrite an unchanged status on the next pass", func() {
			markHealthy(r, initApp, infraApp, checkApp)
			_, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			before := getCluster(r).ResourceVersion
			_, err = reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(getCluster(r).ResourceVersion).To(Equal(before))
		})

		It("refreshes lastCheckTime once it is older than the heartbeat interval", func() {
			markHealthy(r, initApp, infraApp, checkApp)
			_, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			cl := getCluster(r)
			stale := metav1.NewTime(time.Now().Add(-healthHeartbeatInterval - time.Minute))
			cl.Status.Health.LastCheckTime = &stale
			Expect(r.Status().Update(ctx, cl)).To(Succeed())
			before := getCluster(r).ResourceVersion

			_, err = reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			after := getCluster(r)
			Expect(after.ResourceVersion).NotTo(Equal(before))
			Expect(after.Status.Health.LastCheckTime.Time).To(BeTemporally(">", stale.Time))
		})

		It("treats a running Application with an unhealthy component as unhealthy", func() {
			markHealthy(r, initApp, infraApp)
			setAppStatus(r, checkApp, common.ApplicationRunning, false)
			_, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(getCluster(r).Status.Phase).To(Equal(v1beta1.ClusterPhaseValidating))
		})
	})

	Describe("a missing ClusterBlueprintRevision", func() {
		It("reports BlueprintResolved False, stays Pending, renders nothing and requeues", func() {
			r := newTestReconciler(GinkgoT(), clusterWithRefs("bp-missing"))
			res, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(BeNumerically(">", 0))

			cl := getCluster(r)
			Expect(cl.Status.Phase).To(Equal(v1beta1.ClusterPhasePending))
			cond := meta.FindStatusCondition(cl.Status.Conditions, v1beta1.ClusterConditionBlueprintResolved)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Message).To(ContainSubstring("bp-missing"))
			Expect(listApps(r)).To(BeEmpty())
			Expect(cl.Status.Planes).To(BeEmpty())
			Expect(cl.Status.Health).NotTo(BeNil())
			Expect(cl.Status.Health.Status).To(Equal(v1beta1.ClusterHealthStatusProgressing))
			Expect(cl.Status.Health.PlanesTotal).To(Equal(0))
		})

		It("clears planes and health reported for the previous revision", func() {
			r := newTestReconciler(GinkgoT(),
				clusterWithRefs("bp-1"),
				blueprintRev("bp-1", bpPlane("cni", v1beta1.LifecyclePhaseClusterInit)),
				planeRev("cni"),
			)
			_, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			markHealthy(r, "cluster-cni")
			_, err = reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(getCluster(r).Status.Phase).To(Equal(v1beta1.ClusterPhaseReady))

			cl := getCluster(r)
			ref := &v1beta1.PhaseSpec{BlueprintRef: v1beta1.BlueprintReference{Name: "bp-missing"}}
			cl.Spec = v1beta1.ClusterSpec{ClusterInit: ref, PlaneProvisioning: ref, HealthValidation: ref}
			Expect(r.Update(ctx, cl)).To(Succeed())
			_, err = reconcile(r)
			Expect(err).NotTo(HaveOccurred())

			cl = getCluster(r)
			Expect(cl.Status.Phase).To(Equal(v1beta1.ClusterPhasePending))
			Expect(cl.Status.Planes).To(BeEmpty())
			Expect(cl.Status.Health.Status).To(Equal(v1beta1.ClusterHealthStatusProgressing))
			Expect(cl.Status.Health.PlanesTotal).To(Equal(0))
			Expect(listApps(r)).To(HaveLen(1), "the previous revision's Application is kept until a resolvable revision replaces it")
		})
	})

	Describe("a missing ClusterPlaneRevision", func() {
		It("keeps existing Applications, reports the missing revision and requeues", func() {
			r := newTestReconciler(GinkgoT(),
				clusterWithRefs("bp-1"),
				blueprintRev("bp-1",
					bpPlane("cni", v1beta1.LifecyclePhaseClusterInit),
					bpPlane("policy", v1beta1.LifecyclePhaseClusterInit),
				),
				planeRev("cni"), planeRev("policy"),
			)
			_, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(listApps(r)).To(HaveLen(2))

			Expect(r.Delete(ctx, planeRev("policy"))).To(Succeed())
			res, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(res.RequeueAfter).To(BeNumerically(">", 0))

			Expect(listApps(r)).To(HaveLen(2), "the plane's Application must be left alone until the hub re-pushes the revision")
			cl := getCluster(r)
			cond := meta.FindStatusCondition(cl.Status.Conditions, v1beta1.ClusterConditionBlueprintResolved)
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			Expect(cond.Message).To(ContainSubstring("policy-rev"))
			Expect(cl.Status.Planes).To(HaveLen(2))
			Expect(cl.Status.Planes[1].Name).To(Equal("policy"))
			Expect(cl.Status.Planes[1].Application).To(Equal("cluster-policy"))
			Expect(cl.Status.Planes[1].PlaneRevision).To(Equal("policy-rev"),
				"the revision the kept Application was rendered from stays in status while the hub re-pushes it")
		})
	})

	Describe("a newer blueprint revision", func() {
		var r *Reconciler

		BeforeEach(func() {
			r = newTestReconciler(GinkgoT(),
				clusterWithRefs("bp-1"),
				blueprintRev("bp-1",
					bpPlane("cni", v1beta1.LifecyclePhaseClusterInit),
					bpPlane("policy", v1beta1.LifecyclePhaseClusterInit),
				),
				planeRev("cni"), planeRev("policy"),
				blueprintRev("bp-2",
					v1beta1.BlueprintPlaneRevision{Name: "cni", Phase: v1beta1.LifecyclePhaseClusterInit, PlaneRevision: "cni-rev2"},
				),
				&v1beta1.ClusterPlaneRevision{
					ObjectMeta: metav1.ObjectMeta{Name: "cni-rev2", Namespace: testNS},
					Spec: v1beta1.ClusterPlaneRevisionSpec{
						PlaneName: "cni",
						Hash:      "cni-hash-2",
						Components: []v1beta1.ClusterPlaneComponent{
							{Name: "cilium", Type: "helm", Properties: &runtime.RawExtension{Raw: []byte(`{"chart":"cilium"}`)}},
						},
					},
				},
			)
			_, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(listApps(r)).To(HaveLen(2))

			cl := getCluster(r)
			ref := &v1beta1.PhaseSpec{BlueprintRef: v1beta1.BlueprintReference{Name: "bp-2"}}
			cl.Spec = v1beta1.ClusterSpec{ClusterInit: ref, PlaneProvisioning: ref, HealthValidation: ref}
			Expect(r.Update(ctx, cl)).To(Succeed())
			_, err = reconcile(r)
			Expect(err).NotTo(HaveOccurred())
		})

		It("deletes the Application of a dropped plane", func() {
			_, err := getApp(r, "cluster-policy")
			Expect(apierrors.IsNotFound(err)).To(BeTrue(), "cluster-policy should be gone, got %v", err)
			Expect(listApps(r)).To(HaveLen(1))
			Expect(getCluster(r).Status.Planes).To(HaveLen(1))
		})

		It("re-renders a kept plane from its new plane revision in place", func() {
			app := mustGetApp(r, "cluster-cni")
			Expect(app.Spec.Components).To(HaveLen(1))
			Expect(app.Spec.Components[0].Name).To(Equal("cilium"))
		})

		It("does not prune a labelled Application another owner controls", func() {
			foreign := &v1beta1.Application{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cluster-someone-elses",
					Namespace: testNS,
					Labels:    map[string]string{LabelClusterName: testCluster, LabelClusterPlane: "someone-elses"},
				},
			}
			Expect(r.Create(ctx, foreign)).To(Succeed())
			_, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			_, err = getApp(r, "cluster-someone-elses")
			Expect(err).NotTo(HaveOccurred())
			Expect(getCluster(r).Status.Planes).To(HaveLen(1))
		})

		It("leaves an unchanged Application's ResourceVersion alone on the next pass", func() {
			before := mustGetApp(r, "cluster-cni").ResourceVersion
			_, err := reconcile(r)
			Expect(err).NotTo(HaveOccurred())
			Expect(mustGetApp(r, "cluster-cni").ResourceVersion).To(Equal(before))
		})
	})
})
