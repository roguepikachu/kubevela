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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/spokecluster/credential"
)

const bootstrapPlaneName = "spoke-runtime"

// bootstrapSpoke is a connectable spoke that also asks for its runtime to be installed.
func bootstrapSpoke(name string) *v1beta1.SpokeCluster {
	sc := connectableSpoke(name)
	sc.Spec.Bootstrap = &v1beta1.BootstrapSpec{PlaneRef: v1beta1.PlaneReference{Name: bootstrapPlaneName}}
	return sc
}

// bootstrappedSpoke is a spoke an earlier pass left Bootstrapped: projection set and the
// condition True. Callers decide whether spec.bootstrap is still present.
func bootstrappedSpoke(name string) *v1beta1.SpokeCluster {
	sc := bootstrapSpoke(name)
	sc.Status.Bootstrap = &v1beta1.ProvisioningStatus{ApplicationName: bootstrapAppName(sc), Phase: "running", Healthy: true, Message: "2/2 components healthy"}
	setCondition(&sc.Status, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionTrue, reasonBootstrapped, "2/2 components healthy")
	return sc
}

// bootstrapPlane is the two-component runtime plane: Flux first, then vela-core through
// a Flux HelmRelease that depends on it.
func bootstrapPlane(ns string) *v1beta1.ClusterPlane {
	return &v1beta1.ClusterPlane{
		ObjectMeta: metav1.ObjectMeta{Name: bootstrapPlaneName, Namespace: ns},
		Spec: v1beta1.ClusterPlaneSpec{Components: []v1beta1.ClusterPlaneComponent{
			{
				Name:       "flux",
				Type:       "k8s-objects",
				Properties: &runtime.RawExtension{Raw: []byte(`{"objects":[{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"flux-system"}}]}`)},
			},
			{
				Name:       "vela",
				Type:       "helm-flux",
				Properties: &runtime.RawExtension{Raw: []byte(`{"chart":"vela-core","repoURL":"https://charts.kubevela.net/core"}`)},
				DependsOn:  []string{"flux"},
			},
		}},
	}
}

func readBootstrapApp(t GinkgoTInterface, r *Reconciler, sc *v1beta1.SpokeCluster) *v1beta1.Application {
	t.Helper()
	app := &v1beta1.Application{}
	if err := r.Get(context.Background(), client.ObjectKey{Namespace: sc.Namespace, Name: bootstrapAppName(sc)}, app); err != nil {
		t.Fatalf("bootstrap application missing: %v", err)
	}
	return app
}

func bootstrapAppExists(r *Reconciler, sc *v1beta1.SpokeCluster) bool {
	err := r.Get(context.Background(), client.ObjectKey{Namespace: sc.Namespace, Name: bootstrapAppName(sc)}, &v1beta1.Application{})
	if apierrors.IsNotFound(err) {
		return false
	}
	Expect(err).NotTo(HaveOccurred())
	return true
}

// bootstrapAppOwnedBy is a bootstrap Application named for sc but controlled by owner,
// with both runtime components reported healthy and running.
func bootstrapAppOwnedBy(t GinkgoTInterface, sc, owner *v1beta1.SpokeCluster) *v1beta1.Application {
	t.Helper()
	app := &v1beta1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: bootstrapAppName(sc), Namespace: sc.Namespace},
		Spec: v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{
			{Name: "flux", Type: "k8s-objects"},
			{Name: "vela", Type: "helm-flux", DependsOn: []string{"flux"}},
		}},
		Status: common.AppStatus{
			Phase: common.ApplicationRunning,
			Services: []common.ApplicationComponentStatus{
				{Name: "flux", Healthy: true, Message: "objects applied"},
				{Name: "vela", Healthy: true, Message: "HelmRelease vela-core ready"},
			},
		},
	}
	if err := controllerutil.SetControllerReference(owner, app, testScheme(t)); err != nil {
		t.Fatalf("failed to set controller reference: %v", err)
	}
	return app
}

// ownedBootstrapApp is a bootstrap Application as an earlier pass would have left it.
func ownedBootstrapApp(t GinkgoTInterface, sc *v1beta1.SpokeCluster) *v1beta1.Application {
	t.Helper()
	return bootstrapAppOwnedBy(t, sc, sc)
}

// foreignBootstrapApp is a same-named Application another SpokeCluster controls.
func foreignBootstrapApp(t GinkgoTInterface, sc *v1beta1.SpokeCluster) *v1beta1.Application {
	t.Helper()
	other := spokeIn("other", sc.Namespace, v1beta1.SpokeDeletionPolicyDetach)
	return bootstrapAppOwnedBy(t, sc, other)
}

func failingProbe(context.Context, *v1beta1.SpokeCluster) (time.Duration, error) {
	return 0, errors.New("dial tcp: i/o timeout")
}

func eventReasons(rec *recordingRecorder) []string {
	reasons := make([]string, 0, len(rec.events))
	for _, e := range rec.events {
		reasons = append(reasons, string(e.Reason))
	}
	return reasons
}

var _ = DescribeTable("BootstrapAppNameIsDeterministic", func(spokeName, want, wantSuffix string) {
	sc := bootstrapSpoke(spokeName)
	got := bootstrapAppName(sc)
	Expect(validation.IsDNS1123Label(got)).To(BeEmpty())
	if want != "" {
		Expect(got).To(Equal(want))
	}
	if wantSuffix != "" {
		Expect(got).To(HaveSuffix(wantSuffix), "truncation keeps the whole suffix")
		Expect(strings.TrimSuffix(got, wantSuffix)).NotTo(HaveSuffix("-"), "the truncated base keeps no trailing hyphen")
	}
	sc.UID = "another-uid"
	sc.Status.InfraIncarnation = 3
	Expect(bootstrapAppName(sc)).To(Equal(got), "neither the UID nor the incarnation reaches the name")
},
	Entry("a short spoke name is used verbatim", "cpspoke1", "sc-cpspoke1-bootstrap", ""),
	Entry("a long spoke name is truncated around the suffix",
		"a-very-long-spoke-name-that-pushes-the-application-name-past-the-limit", "", "-bootstrap"),
	Entry("a cut landing on a hyphen loses that hyphen too",
		"a-very-long-spoke-name-that-pushes-the-application-nam-e-past-the-limit", "", "-bootstrap"),
)

var _ = It("BootstrapRendersPlaneIntoGatewayTargetedApplication", func() {
	t := GinkgoT()
	sc := bootstrapSpoke("cpspoke1")
	r := connectedReconciler(t, sc, bootstrapPlane(sc.Namespace))

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	app := readBootstrapApp(t, r, sc)
	Expect(app.Name).To(Equal("sc-cpspoke1-bootstrap"))
	Expect(app.OwnerReferences).To(HaveLen(1))
	Expect(*app.OwnerReferences[0].Controller).To(BeTrue())
	Expect(app.OwnerReferences[0].Kind).To(Equal("SpokeCluster"))
	Expect(app.Labels).To(HaveKeyWithValue(labelSpokeName, "cpspoke1"))
	Expect(app.Labels).To(HaveKeyWithValue(labelSpokeRole, roleBootstrap))
	Expect(app.Labels).To(HaveKeyWithValue(labelSpokeUID, string(sc.UID)))

	Expect(app.Spec.Components).To(HaveLen(2))
	Expect(app.Spec.Components[0].Name).To(Equal("flux"), "bootstrap keeps the plane's own component names")
	Expect(app.Spec.Components[0].Type).To(Equal("k8s-objects"))
	Expect(app.Spec.Components[0].DependsOn).To(BeEmpty())
	Expect(app.Spec.Components[1].Name).To(Equal("vela"))
	Expect(app.Spec.Components[1].Type).To(Equal("helm-flux"))
	Expect(app.Spec.Components[1].DependsOn).To(Equal([]string{"flux"}))
	Expect(string(app.Spec.Components[1].Properties.Raw)).To(MatchJSON(`{"chart":"vela-core","repoURL":"https://charts.kubevela.net/core"}`))

	Expect(app.Spec.Policies).To(HaveLen(2))
	Expect(app.Spec.Policies[0].Type).To(Equal("topology"))
	Expect(string(app.Spec.Policies[0].Properties.Raw)).To(MatchJSON(`{"clusters":["cpspoke1"]}`))
	Expect(app.Spec.Policies[1].Type).To(Equal("garbage-collect"))
	Expect(string(app.Spec.Policies[1].Properties.Raw)).To(MatchJSON(`{"rules":[{"selector":{"componentNames":["flux","vela"]},"strategy":"never"}]}`))
	for _, p := range app.Spec.Policies {
		Expect(p.Type).NotTo(BeElementOf("apply-once", "take-over"), "the runtime is re-applied, not applied once or adopted")
	}

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionTrue, reasonProbeSucceeded)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapping)
	Expect(latest.Status.Bootstrap).NotTo(BeNil())
	Expect(latest.Status.Bootstrap.ApplicationName).To(Equal("sc-cpspoke1-bootstrap"))
	Expect(latest.Status.Bootstrap.Healthy).To(BeFalse())
})

type bootstrapHealthCase struct {
	phase      common.ApplicationPhase
	velaHealth bool
	wantStatus metav1.ConditionStatus
	wantReason string
}

var _ = DescribeTable("BootstrapProjectsApplicationHealth", func(tc bootstrapHealthCase) {
	t := GinkgoT()
	sc := bootstrapSpoke("cpspoke1")
	app := ownedBootstrapApp(t, sc)
	app.Status.Phase = tc.phase
	app.Status.Services[1].Healthy = tc.velaHealth
	r := connectedReconciler(t, sc, bootstrapPlane(sc.Namespace), app)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionBootstrapped, tc.wantStatus, tc.wantReason)
	Expect(latest.Status.Bootstrap).NotTo(BeNil())
	Expect(latest.Status.Bootstrap.ApplicationName).To(Equal("sc-cpspoke1-bootstrap"))
	Expect(latest.Status.Bootstrap.Phase).To(Equal(string(tc.phase)))
	Expect(latest.Status.Bootstrap.Healthy).To(Equal(tc.wantStatus == metav1.ConditionTrue))
	Expect(latest.Status.Bootstrap.Message).To(ContainSubstring("components healthy"))
	cond := meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionBootstrapped)
	Expect(cond.Message).To(Equal(latest.Status.Bootstrap.Message), "the condition carries the same summary as the projection")
},
	Entry("all services healthy and running is Bootstrapped",
		bootstrapHealthCase{phase: common.ApplicationRunning, velaHealth: true, wantStatus: metav1.ConditionTrue, wantReason: reasonBootstrapped}),
	Entry("one unhealthy service is still Bootstrapping",
		bootstrapHealthCase{phase: common.ApplicationRunning, velaHealth: false, wantStatus: metav1.ConditionFalse, wantReason: reasonBootstrapping}),
	Entry("a failed workflow is BootstrapFailed",
		bootstrapHealthCase{phase: common.ApplicationWorkflowFailed, velaHealth: false, wantStatus: metav1.ConditionFalse, wantReason: reasonBootstrapFailed}),
)

// A plane that disappears after a successful bootstrap must not leave the projection
// claiming healthy under a False condition.
var _ = It("BootstrapMissingPlaneNeverStopsProbing", func() {
	t := GinkgoT()
	sc := bootstrappedSpoke("cpspoke1")
	r := connectedReconciler(t, sc, ownedBootstrapApp(t, sc))

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred(), "bootstrap problems are state to report, not a controller fault")

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionTrue, reasonProbeSucceeded)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapPlaneUnresolved)
	cond := meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionBootstrapped)
	Expect(cond.Message).To(ContainSubstring("reading ClusterPlane " + sc.Namespace + "/" + bootstrapPlaneName))
	Expect(cond.Message).To(ContainSubstring("not found"))
	Expect(latest.Status.Bootstrap).NotTo(BeNil())
	Expect(latest.Status.Bootstrap.ApplicationName).To(Equal("sc-cpspoke1-bootstrap"), "the known Application name is kept")
	Expect(latest.Status.Bootstrap.Healthy).To(BeFalse(), "no stale healthy under a False condition")
	Expect(latest.Status.Bootstrap.Message).To(Equal(cond.Message))
	Expect(bootstrapAppExists(r, sc)).To(BeTrue(), "an unresolved plane does not touch the Application already rendered")
})

var _ = It("BootstrapEmptyPlaneIsUnresolved", func() {
	t := GinkgoT()
	sc := bootstrapSpoke("cpspoke1")
	plane := bootstrapPlane(sc.Namespace)
	plane.Spec.Components = nil
	r := connectedReconciler(t, sc, plane)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapPlaneUnresolved)
	cond := meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionBootstrapped)
	Expect(cond.Message).To(ContainSubstring("has no components"))
	Expect(latest.Status.Bootstrap).NotTo(BeNil())
	Expect(latest.Status.Bootstrap.ApplicationName).To(BeEmpty(), "nothing was rendered, so no name is known")
	Expect(latest.Status.Bootstrap.Healthy).To(BeFalse())
	Expect(latest.Status.Bootstrap.Message).To(Equal(cond.Message))
	Expect(bootstrapAppExists(r, sc)).To(BeFalse())
})

var _ = It("BootstrapRemovedFromSpecDeletesApplication", func() {
	t := GinkgoT()
	sc := bootstrappedSpoke("cpspoke1")
	app := ownedBootstrapApp(t, sc)
	sc.Spec.Bootstrap = nil
	r := connectedReconciler(t, sc, bootstrapPlane(sc.Namespace), app)
	rec := &recordingRecorder{}
	r.record = rec

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	Expect(bootstrapAppExists(r, sc)).To(BeFalse(), "the Application goes with the spec block")
	latest := readSpoke(t, r, sc)
	Expect(meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionBootstrapped)).To(BeNil())
	Expect(latest.Status.Bootstrap).To(BeNil())
	wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionTrue, reasonProbeSucceeded)
	Expect(eventReasons(rec)).To(ContainElement(reasonBootstrapReleased))
})

// Forgetting must not wait for a probe: a spoke that is down when spec.bootstrap is
// removed would otherwise keep its Application and a stale Bootstrapped=True.
var _ = It("BootstrapRemovedFromSpecIsForgottenWhileDisconnected", func() {
	t := GinkgoT()
	sc := bootstrappedSpoke("cpspoke1")
	app := ownedBootstrapApp(t, sc)
	sc.Spec.Bootstrap = nil
	r := connectedReconciler(t, sc, bootstrapPlane(sc.Namespace), app)
	r.probeFn = failingProbe

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionFalse, reasonProbeFailed)
	Expect(bootstrapAppExists(r, sc)).To(BeFalse())
	Expect(meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionBootstrapped)).To(BeNil())
	Expect(latest.Status.Bootstrap).To(BeNil())
})

var _ = It("BootstrapForgetLeavesForeignApplicationAlone", func() {
	t := GinkgoT()
	sc := bootstrappedSpoke("cpspoke1")
	app := foreignBootstrapApp(t, sc)
	sc.Spec.Bootstrap = nil
	r := connectedReconciler(t, sc, app)
	rec := &recordingRecorder{}
	r.record = rec

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	Expect(bootstrapAppExists(r, sc)).To(BeTrue(), "another owner's Application is not this spoke's to delete")
	latest := readSpoke(t, r, sc)
	Expect(meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionBootstrapped)).To(BeNil())
	Expect(latest.Status.Bootstrap).To(BeNil(), "the projection is still dropped: this spoke no longer claims a runtime")
	Expect(eventReasons(rec)).NotTo(ContainElement(reasonBootstrapReleased), "nothing was released")
})

var _ = It("BootstrapFailsWhenApplicationIsOwnedElsewhere", func() {
	t := GinkgoT()
	sc := bootstrapSpoke("cpspoke1")
	app := foreignBootstrapApp(t, sc)
	r := connectedReconciler(t, sc, bootstrapPlane(sc.Namespace), app)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionTrue, reasonProbeSucceeded)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapFailed)
	cond := meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionBootstrapped)
	Expect(cond.Message).To(ContainSubstring("already owned"))
	Expect(latest.Status.Bootstrap).NotTo(BeNil())
	Expect(latest.Status.Bootstrap.ApplicationName).To(Equal("sc-cpspoke1-bootstrap"), "the name this pass tried to apply")
	Expect(latest.Status.Bootstrap.Healthy).To(BeFalse())
	Expect(latest.Status.Bootstrap.Message).To(Equal(cond.Message))

	kept := readBootstrapApp(t, r, sc)
	Expect(kept.OwnerReferences[0].Name).To(Equal("other"), "the foreign Application is left as it was")
	Expect(kept.Labels).NotTo(HaveKey(labelSpokeRole))
})

var _ = It("BootstrapWaitsForTerminatingApplication", func() {
	t := GinkgoT()
	sc := bootstrappedSpoke("cpspoke1")
	app := ownedBootstrapApp(t, sc)
	now := metav1.Now()
	app.DeletionTimestamp = &now
	app.Finalizers = []string{"test.kubevela.io/hold"}
	r := connectedReconciler(t, sc, bootstrapPlane(sc.Namespace), app)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapping)
	cond := meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionBootstrapped)
	Expect(cond.Message).To(Equal("previous bootstrap Application is terminating"))
	Expect(latest.Status.Bootstrap).NotTo(BeNil())
	Expect(latest.Status.Bootstrap.ApplicationName).To(Equal("sc-cpspoke1-bootstrap"))
	Expect(latest.Status.Bootstrap.Healthy).To(BeFalse())

	untouched := readBootstrapApp(t, r, sc)
	Expect(untouched.ResourceVersion).To(Equal(app.ResourceVersion), "a terminating Application is not mutated")
	Expect(untouched.Labels).NotTo(HaveKey(labelSpokeRole))
})

var _ = It("BootstrapWithoutSpecOrProjectionTouchesNothing", func() {
	t := GinkgoT()
	sc := connectableSpoke("cpspoke1")
	r := connectedReconciler(t, sc)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	Expect(meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionBootstrapped)).To(BeNil())
	Expect(latest.Status.Bootstrap).To(BeNil())
	Expect(bootstrapAppExists(r, sc)).To(BeFalse())
})

// The render arm runs only on a Connected pass: a failed probe renders nothing and leaves
// whatever Bootstrapped said before in place.
var _ = It("BootstrapRenderSkippedWhenProbeFails", func() {
	t := GinkgoT()
	sc := bootstrappedSpoke("cpspoke1")
	r := connectedReconciler(t, sc, bootstrapPlane(sc.Namespace))
	r.probeFn = failingProbe

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionFalse, reasonProbeFailed)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionTrue, reasonBootstrapped)
	Expect(latest.Status.Bootstrap).To(Equal(sc.Status.Bootstrap), "the projection is left as the last Connected pass wrote it")
	Expect(bootstrapAppExists(r, sc)).To(BeFalse(), "an unreachable spoke gets no runtime rendered at it")
})

var _ = It("BootstrapIsReappliedInPlace", func() {
	t := GinkgoT()
	sc := bootstrapSpoke("cpspoke1")
	r := connectedReconciler(t, sc, bootstrapPlane(sc.Namespace))

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())
	first := readBootstrapApp(t, r, sc)

	_, err = reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())
	second := readBootstrapApp(t, r, sc)
	Expect(second.UID).To(Equal(first.UID), "the same Application is updated, never recreated")
	Expect(second.ResourceVersion).To(Equal(first.ResourceVersion), "an unchanged render issues no Update")

	apps := &v1beta1.ApplicationList{}
	Expect(r.List(context.Background(), apps, client.InNamespace(sc.Namespace))).To(Succeed())
	Expect(apps.Items).To(HaveLen(1))
})

// The hook lives at the end of the connect sequence, so a provision spoke reaches it on the
// pass that hands over, without any provision-specific wiring.
var _ = It("ProvisionHandOverAlsoBootstraps", func() {
	t := GinkgoT()
	sc := provisionSpoke("cpspoke1")
	sc.Spec.Bootstrap = &v1beta1.BootstrapSpec{PlaneRef: v1beta1.PlaneReference{Name: bootstrapPlaneName}}
	bp, plane := infraBlueprint(sc.Namespace)
	r := newTestReconciler(t, sc, bp, plane, bootstrapPlane(sc.Namespace))
	r.Providers = kubeconfigRegistry(tokenCredential(), nil)
	r.probeFn = func(_ context.Context, _ *v1beta1.SpokeCluster) (time.Duration, error) {
		return 5 * time.Millisecond, nil
	}
	r.discoverFn = func(_ context.Context, _ *v1beta1.SpokeCluster, _ *credential.Materialized, latency time.Duration) (*v1beta1.SpokeClusterInfo, error) {
		return &v1beta1.SpokeClusterInfo{KubernetesVersion: "v1.35.6-eks", NodeCount: 2, LatencyMillis: latency.Milliseconds()}, nil
	}

	By("rendering the infra Application and not yet the runtime", func() {
		_, err := reconcileOnce(t, r, sc)
		Expect(err).NotTo(HaveOccurred())
		latest := readSpoke(t, r, sc)
		wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonProvisioning)
		Expect(bootstrapAppExists(r, sc)).To(BeFalse(), "no cluster yet, so nothing to install a runtime on")
	})

	By("the substrate finishing", func() {
		app := readInfraApp(t, r, sc)
		app.Status.Phase = common.ApplicationRunning
		app.Status.Services = []common.ApplicationComponentStatus{{Name: "foundation-cluster", Healthy: true}}
		Expect(r.Status().Update(context.Background(), app)).To(Succeed())
	})

	By("the hand-over pass rendering the bootstrap Application", func() {
		_, err := reconcileOnce(t, r, sc)
		Expect(err).NotTo(HaveOccurred())
		latest := readSpoke(t, r, sc)
		wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionTrue, reasonInfraReady)
		wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionTrue, reasonProbeSucceeded)
		wantCondition(t, latest, v1beta1.SpokeClusterConditionBootstrapped, metav1.ConditionFalse, reasonBootstrapping)
		Expect(latest.Status.Provisioning).NotTo(BeNil())
		Expect(latest.Status.Bootstrap).NotTo(BeNil())
		app := readBootstrapApp(t, r, sc)
		Expect(app.Spec.Components).To(HaveLen(2))
		Expect(app.Spec.Policies[0].Type).To(Equal("topology"))
	})
})
