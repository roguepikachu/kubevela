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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	workflowv1alpha1 "github.com/kubevela/workflow/api/v1alpha1"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/spokecluster/credential"
)

// provisionSpoke is a connectable spoke switched to mode provision with the eks blueprint.
func provisionSpoke(name string) *v1beta1.SpokeCluster {
	sc := connectableSpoke(name)
	sc.Spec.Mode = v1beta1.SpokeClusterModeProvision
	sc.Spec.InfraProvisioning = &v1beta1.InfraProvisioning{BlueprintRef: &v1beta1.BlueprintReference{Name: "eks"}}
	return sc
}

// infraBlueprint is a one-plane, one-component blueprint in the spoke's namespace.
func infraBlueprint(ns string) (*v1beta1.ClusterBlueprint, *v1beta1.ClusterPlane) {
	plane := &v1beta1.ClusterPlane{
		ObjectMeta: metav1.ObjectMeta{Name: "eks-foundation", Namespace: ns},
		Spec: v1beta1.ClusterPlaneSpec{Components: []v1beta1.ClusterPlaneComponent{{
			Name:       "cluster",
			Type:       "eks-capi",
			Properties: &runtime.RawExtension{Raw: []byte(`{"name":"cpspoke1","region":"us-west-2"}`)},
		}}},
	}
	bp := &v1beta1.ClusterBlueprint{
		ObjectMeta: metav1.ObjectMeta{Name: "eks", Namespace: ns},
		Spec: v1beta1.ClusterBlueprintSpec{Planes: []v1beta1.BlueprintPlane{{
			Name: "foundation",
			Ref:  v1beta1.BlueprintReference{Name: "eks-foundation"},
		}}},
	}
	return bp, plane
}

func readInfraApp(t GinkgoTInterface, r *Reconciler, sc *v1beta1.SpokeCluster) *v1beta1.Application {
	t.Helper()
	app := &v1beta1.Application{}
	err := r.Get(context.Background(), client.ObjectKey{Namespace: sc.Namespace, Name: infraAppName(sc)}, app)
	if err != nil {
		t.Fatalf("infra application missing: %v", err)
	}
	return app
}

func healthyInfraApp(sc *v1beta1.SpokeCluster) *v1beta1.Application {
	return &v1beta1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: infraAppName(sc), Namespace: sc.Namespace},
		Spec:       v1beta1.ApplicationSpec{Components: []common.ApplicationComponent{{Name: "foundation-cluster", Type: "eks-capi"}}},
		Status: common.AppStatus{
			Phase:    common.ApplicationRunning,
			Services: []common.ApplicationComponentStatus{{Name: "foundation-cluster", Healthy: true, Message: "EKS cpspoke1 ready=true"}},
		},
	}
}

var _ = It("ProvisionRendersInfraApplicationAndWaits", func() {
	t := GinkgoT()
	sc := provisionSpoke("cpspoke1")
	bp, plane := infraBlueprint(sc.Namespace)
	r := newTestReconciler(t, sc, bp, plane)

	res, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())
	Expect(res.RequeueAfter).To(Equal(provisionRequeue))

	app := readInfraApp(t, r, sc)
	Expect(app.Spec.Components).To(HaveLen(1))
	Expect(app.Spec.Components[0].Name).To(Equal("foundation-cluster"))
	Expect(app.Spec.Components[0].Type).To(Equal("eks-capi"))
	Expect(string(app.Spec.Components[0].Properties.Raw)).To(MatchJSON(`{"name":"cpspoke1","region":"us-west-2"}`))
	Expect(app.Labels).To(HaveKeyWithValue(labelSpokeName, "cpspoke1"))
	Expect(app.OwnerReferences).To(HaveLen(1))
	Expect(*app.OwnerReferences[0].Controller).To(BeTrue())
	Expect(app.OwnerReferences[0].Kind).To(Equal("SpokeCluster"))

	policies := map[string]string{}
	for _, p := range app.Spec.Policies {
		policies[p.Type] = string(p.Properties.Raw)
	}
	Expect(policies["apply-once"]).To(MatchJSON(`{"enable":true}`))
	Expect(policies).NotTo(HaveKey("take-over"), "provision must not claim objects it did not create")
	Expect(policies["garbage-collect"]).To(MatchJSON(`{"rules":[{"selector":{"componentNames":["foundation-cluster"]},"strategy":"never"}]}`))

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonProvisioning)
	Expect(latest.Status.Connection).To(Equal(v1beta1.ConnectionStateUnknown))
	Expect(latest.Status.Provisioning).NotTo(BeNil())
	Expect(latest.Status.Provisioning.ApplicationName).To(Equal("sc-cpspoke1-infra"))
	Expect(latest.Status.Provisioning.Healthy).To(BeFalse())
	Expect(secretExists(t, r.Client, "cpspoke1")).To(BeFalse(), "no gateway secret before the cluster exists")
})

var _ = It("ProvisionDeletePolicyDeleteOmitsRetainRule", func() {
	t := GinkgoT()
	sc := provisionSpoke("cpspoke1")
	sc.Spec.InfraDeletionPolicy = v1beta1.InfraDeletionPolicyDelete
	bp, plane := infraBlueprint(sc.Namespace)
	r := newTestReconciler(t, sc, bp, plane)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	app := readInfraApp(t, r, sc)
	types := []string{}
	for _, p := range app.Spec.Policies {
		types = append(types, p.Type)
	}
	Expect(types).To(ConsistOf("apply-once"))
})

var _ = It("AdoptRendersWithTakeOver", func() {
	t := GinkgoT()
	sc := provisionSpoke("cpspoke1")
	sc.Spec.Mode = v1beta1.SpokeClusterModeAdopt
	bp, plane := infraBlueprint(sc.Namespace)
	r := newTestReconciler(t, sc, bp, plane)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	app := readInfraApp(t, r, sc)
	policies := map[string]string{}
	for _, p := range app.Spec.Policies {
		policies[p.Type] = string(p.Properties.Raw)
	}
	Expect(policies["apply-once"]).To(MatchJSON(`{"enable":true}`))
	Expect(policies["take-over"]).To(MatchJSON(`{"rules":[{"selector":{"componentNames":["foundation-cluster"]}}]}`), "adopt claims the existing substrate objects")
	Expect(policies["garbage-collect"]).To(MatchJSON(`{"rules":[{"selector":{"componentNames":["foundation-cluster"]},"strategy":"never"}]}`))
})

// unmanagedExistsInfraApp is an infra Application whose dispatch vela-core refused because
// the substrate objects already exist and belong to no application.
func unmanagedExistsInfraApp(sc *v1beta1.SpokeCluster) *v1beta1.Application {
	app := healthyInfraApp(sc)
	app.Status.Phase = common.ApplicationWorkflowFailed
	app.Status.Services = []common.ApplicationComponentStatus{{Name: "foundation-cluster", Healthy: false}}
	app.Status.Workflow = &common.WorkflowStatus{
		Steps: []workflowv1alpha1.WorkflowStepStatus{{StepStatus: workflowv1alpha1.StepStatus{
			ID:   "step-1",
			Name: "foundation-cluster",
			Type: "apply-component",
			Message: "apply-component failed: Dispatch: pre-dispatch dryrun failed: Found 6 errors. " +
				"[(cannot apply ApplyOption: AWSManagedControlPlane vela-system/cpspoke1-control-plane exists but not managed by any application now)]",
		}}},
	}
	return app
}

var _ = It("ProvisionReportsUnmanagedExists", func() {
	t := GinkgoT()
	sc := provisionSpoke("cpspoke1")
	bp, plane := infraBlueprint(sc.Namespace)
	r := newTestReconciler(t, sc, bp, plane, unmanagedExistsInfraApp(sc))

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonInfraUnmanagedExists)
	cond := meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionInfraProvisioned)
	Expect(cond.Message).To(ContainSubstring("mode adopt"))
	Expect(latest.Status.Provisioning.Message).To(ContainSubstring("mode adopt"))
})

var _ = It("ProvisionReportsUnmanagedExistsBeforeWorkflowFails", func() {
	t := GinkgoT()
	sc := provisionSpoke("cpspoke1")
	bp, plane := infraBlueprint(sc.Namespace)
	app := unmanagedExistsInfraApp(sc)
	app.Status.Phase = common.ApplicationRunningWorkflow
	r := newTestReconciler(t, sc, bp, plane, app)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonInfraUnmanagedExists)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionUnknown, reasonInfraUnmanagedExists)
})

var _ = It("ProvisionSuspendedWorkflowReportsUnhealthy", func() {
	t := GinkgoT()
	sc := provisionSpoke("cpspoke1")
	bp, plane := infraBlueprint(sc.Namespace)
	app := healthyInfraApp(sc)
	app.Status.Phase = common.ApplicationWorkflowSuspending
	app.Status.Services = []common.ApplicationComponentStatus{{Name: "foundation-cluster", Healthy: false}}
	r := newTestReconciler(t, sc, bp, plane, app)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonInfraUnhealthy)
})

var _ = It("AdoptDoesNotReportUnmanagedExists", func() {
	t := GinkgoT()
	sc := provisionSpoke("cpspoke1")
	sc.Spec.Mode = v1beta1.SpokeClusterModeAdopt
	bp, plane := infraBlueprint(sc.Namespace)
	r := newTestReconciler(t, sc, bp, plane, unmanagedExistsInfraApp(sc))

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonInfraUnhealthy)
})

var _ = It("ProvisionHealthyHandsOverToConnect", func() {
	t := GinkgoT()
	sc := provisionSpoke("cpspoke1")
	bp, plane := infraBlueprint(sc.Namespace)
	r := newTestReconciler(t, sc, bp, plane, healthyInfraApp(sc))
	r.Providers = kubeconfigRegistry(tokenCredential(), nil)
	r.probeFn = func(_ context.Context, _ *v1beta1.SpokeCluster) (time.Duration, error) {
		return 5 * time.Millisecond, nil
	}
	r.discoverFn = func(_ context.Context, _ *v1beta1.SpokeCluster, _ *credential.Materialized, latency time.Duration) (*v1beta1.SpokeClusterInfo, error) {
		return &v1beta1.SpokeClusterInfo{KubernetesVersion: "v1.35.6-eks", NodeCount: 2, LatencyMillis: latency.Milliseconds()}, nil
	}

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionTrue, reasonInfraReady)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionTrue, reasonProbeSucceeded)
	Expect(latest.Status.Connection).To(Equal(v1beta1.ConnectionStateConnected))
	Expect(latest.Status.Provisioning.Healthy).To(BeTrue())
	Expect(latest.Status.Provisioning.Phase).To(Equal("running"))
	Expect(latest.Status.ClusterInfo.NodeCount).To(Equal(2))
	Expect(secretExists(t, r.Client, "cpspoke1")).To(BeTrue(), "connect path registered the gateway secret")
})

var _ = It("ProvisionMissingBlueprintReportsUnresolved", func() {
	t := GinkgoT()
	sc := provisionSpoke("cpspoke1")
	r := newTestReconciler(t, sc)

	res, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())
	Expect(res.RequeueAfter).To(Equal(provisionRequeue))

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonBlueprintUnresolved)
	app := &v1beta1.Application{}
	Expect(r.Get(context.Background(), client.ObjectKey{Namespace: sc.Namespace, Name: infraAppName(sc)}, app)).NotTo(Succeed())
})

var _ = It("ProvisionRejectsPlaneLevelDependsOn", func() {
	t := GinkgoT()
	sc := provisionSpoke("cpspoke1")
	bp, plane := infraBlueprint(sc.Namespace)
	bp.Spec.Planes[0].DependsOn = []string{"network"}
	r := newTestReconciler(t, sc, bp, plane)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())
	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonBlueprintUnresolved)
})

var _ = It("ProvisionRejectsPlaneRevisionPin", func() {
	t := GinkgoT()
	sc := provisionSpoke("cpspoke1")
	bp, plane := infraBlueprint(sc.Namespace)
	bp.Spec.Planes[0].Ref.Revision = "eks-foundation-v1"
	r := newTestReconciler(t, sc, bp, plane)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())
	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonBlueprintUnresolved)
	Expect(meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionInfraProvisioned).Message).To(ContainSubstring("ref.revision"))
})

var _ = It("ProvisionIsIdempotentAcrossPasses", func() {
	t := GinkgoT()
	sc := provisionSpoke("cpspoke1")
	bp, plane := infraBlueprint(sc.Namespace)
	r := newTestReconciler(t, sc, bp, plane)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())
	first := readInfraApp(t, r, sc)
	_, err = reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())
	second := readInfraApp(t, r, sc)
	Expect(second.UID).To(Equal(first.UID), "the same Application is updated, never recreated")
	Expect(second.ResourceVersion).To(Equal(first.ResourceVersion), "an unchanged render issues no Update")

	raw, _ := json.Marshal(second.Spec)
	Expect(string(raw)).To(ContainSubstring(`"foundation-cluster"`))
})

var _ = It("ProvisionRejectsInvalidCredentialWithoutRendering", func() {
	t := GinkgoT()
	sc := provisionSpoke("cpspoke1")
	sc.Spec.Credential.Kubeconfig.SecretRef.Namespace = "other-ns"
	bp, plane := infraBlueprint(sc.Namespace)
	r := newTestReconciler(t, sc, bp, plane)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonSpecInvalid)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionCredentialValid, metav1.ConditionFalse, reasonSpecInvalid)
	Expect(latest.Status.Connection).To(Equal(v1beta1.ConnectionStateUnknown))
	app := &v1beta1.Application{}
	err = r.Get(context.Background(), client.ObjectKey{Namespace: sc.Namespace, Name: infraAppName(sc)}, app)
	Expect(apierrors.IsNotFound(err)).To(BeTrue(), "no infrastructure may be rendered for a spec admission rejects")
})

var _ = It("ProvisionWorkflowFailedReportsUnhealthy", func() {
	t := GinkgoT()
	sc := provisionSpoke("cpspoke1")
	bp, plane := infraBlueprint(sc.Namespace)
	app := healthyInfraApp(sc)
	app.Status.Phase = common.ApplicationWorkflowFailed
	app.Status.Services = []common.ApplicationComponentStatus{{
		Name: "foundation-cluster", Healthy: false, Message: "AWSManagedControlPlane rejected: subnet not found",
	}}
	r := newTestReconciler(t, sc, bp, plane, app)

	res, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())
	Expect(res.RequeueAfter).To(Equal(provisionRequeue))

	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonInfraUnhealthy)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionUnknown, reasonInfraUnhealthy)
	Expect(latest.Status.Provisioning.Healthy).To(BeFalse())
	Expect(latest.Status.Provisioning.Phase).To(Equal("workflowFailed"))
	Expect(latest.Status.Provisioning.Message).To(ContainSubstring("subnet not found"))
})

var _ = It("ProvisionTwoPassHandOverKeepsProjection", func() {
	t := GinkgoT()
	sc := provisionSpoke("cpspoke1")
	bp, plane := infraBlueprint(sc.Namespace)
	r := newTestReconciler(t, sc, bp, plane)
	r.Providers = kubeconfigRegistry(tokenCredential(), nil)
	r.probeFn = func(_ context.Context, _ *v1beta1.SpokeCluster) (time.Duration, error) {
		return 5 * time.Millisecond, nil
	}
	r.discoverFn = func(_ context.Context, _ *v1beta1.SpokeCluster, _ *credential.Materialized, latency time.Duration) (*v1beta1.SpokeClusterInfo, error) {
		return &v1beta1.SpokeClusterInfo{KubernetesVersion: "v1.35.6-eks", NodeCount: 2, LatencyMillis: latency.Milliseconds()}, nil
	}

	By("rendering the Application and waiting while it is unhealthy", func() {
		_, err := reconcileOnce(t, r, sc)
		Expect(err).NotTo(HaveOccurred())
		latest := readSpoke(t, r, sc)
		wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonProvisioning)
		Expect(secretExists(t, r.Client, "cpspoke1")).To(BeFalse())
	})

	By("the substrate finishing and vela-core marking the Application running", func() {
		app := readInfraApp(t, r, sc)
		app.Status.Phase = common.ApplicationRunning
		app.Status.Services = []common.ApplicationComponentStatus{{Name: "foundation-cluster", Healthy: true, Message: "EKS cpspoke1 ready=true"}}
		Expect(r.Status().Update(context.Background(), app)).To(Succeed())
	})

	By("the next pass handing over to connect without losing the provisioning projection", func() {
		_, err := reconcileOnce(t, r, sc)
		Expect(err).NotTo(HaveOccurred())
		latest := readSpoke(t, r, sc)
		wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionTrue, reasonInfraReady)
		wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionTrue, reasonProbeSucceeded)
		Expect(latest.Status.Connection).To(Equal(v1beta1.ConnectionStateConnected))
		Expect(latest.Status.Provisioning).NotTo(BeNil())
		Expect(latest.Status.Provisioning.Healthy).To(BeTrue())
		Expect(latest.Status.ClusterInfo.NodeCount).To(Equal(2))
		Expect(secretExists(t, r.Client, "cpspoke1")).To(BeTrue())
	})
})
