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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/spokecluster/credential"
)

// releasingSpoke is a connect spoke that used to manage infrastructure: its status still
// carries the provisioning projection and a True InfraProvisioned condition from the
// previous mode.
func releasingSpoke(name string) *v1beta1.SpokeCluster {
	sc := connectableSpoke(name)
	sc.Status.Provisioning = &v1beta1.ProvisioningStatus{ApplicationName: infraAppName(sc), Healthy: true, Phase: "running"}
	meta.SetStatusCondition(&sc.Status.Conditions, metav1.Condition{
		Type:    v1beta1.SpokeClusterConditionInfraProvisioned,
		Status:  metav1.ConditionTrue,
		Reason:  reasonInfraReady,
		Message: "1/1 components healthy",
	})
	return sc
}

// retainedInfraApp is the infra Application a provision or adopt pass renders when
// infraDeletionPolicy is retain: apply-once plus a garbage-collect rule that never recycles
// the substrate objects.
func retainedInfraApp(sc *v1beta1.SpokeCluster) *v1beta1.Application {
	app := unretainedInfraApp(sc)
	app.Spec.Policies = append(app.Spec.Policies, v1beta1.AppPolicy{
		Name: "retain-infra", Type: "garbage-collect",
		Properties: &runtime.RawExtension{Raw: []byte(`{"rules":[{"selector":{"componentNames":["foundation-cluster"]},"strategy":"never"}]}`)},
	})
	return app
}

// unretainedInfraApp is the infra Application rendered under infraDeletionPolicy delete:
// apply-once only, so deleting it would take the cluster with it.
func unretainedInfraApp(sc *v1beta1.SpokeCluster) *v1beta1.Application {
	return &v1beta1.Application{
		ObjectMeta: metav1.ObjectMeta{Name: infraAppName(sc), Namespace: sc.Namespace},
		Spec: v1beta1.ApplicationSpec{
			Components: []common.ApplicationComponent{{Name: "foundation-cluster", Type: "eks-capi"}},
			Policies: []v1beta1.AppPolicy{{
				Name: "apply-once", Type: "apply-once", Properties: &runtime.RawExtension{Raw: []byte(`{"enable":true}`)},
			}},
		},
	}
}

// infraAppExists reports whether the infra Application for sc is still on the hub.
func infraAppExists(t GinkgoTInterface, r *Reconciler, sc *v1beta1.SpokeCluster) bool {
	t.Helper()
	err := r.Get(context.Background(), client.ObjectKey{Namespace: sc.Namespace, Name: infraAppName(sc)}, &v1beta1.Application{})
	switch {
	case err == nil:
		return true
	case apierrors.IsNotFound(err):
		return false
	default:
		t.Fatalf("reading infra application: %v", err)
		return false
	}
}

// wireHealthySpoke gives the reconciler a working credential, probe and discovery so a
// connect pass reaches Connected.
func wireHealthySpoke(r *Reconciler) {
	r.Providers = kubeconfigRegistry(tokenCredential(), nil)
	r.probeFn = func(_ context.Context, _ *v1beta1.SpokeCluster) (time.Duration, error) {
		return 5 * time.Millisecond, nil
	}
	r.discoverFn = func(_ context.Context, _ *v1beta1.SpokeCluster, _ *credential.Materialized, latency time.Duration) (*v1beta1.SpokeClusterInfo, error) {
		return &v1beta1.SpokeClusterInfo{KubernetesVersion: "v1.35.6-eks", NodeCount: 2, LatencyMillis: latency.Milliseconds()}, nil
	}
}

var _ = It("ReleaseDeletesInfraApplicationThenConnects", func() {
	t := GinkgoT()
	sc := releasingSpoke("cpspoke1")
	r := newTestReconciler(t, sc, retainedInfraApp(sc))
	wireHealthySpoke(r)

	By("the first pass deleting the Application under its retain rule", func() {
		res, err := reconcileOnce(t, r, sc)
		Expect(err).NotTo(HaveOccurred())
		Expect(res.RequeueAfter).To(Equal(provisionRequeue))
		Expect(infraAppExists(t, r, sc)).To(BeFalse(), "the infra Application must be deleted")
		latest := readSpoke(t, r, sc)
		wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonReleasing)
		Expect(latest.Status.Connection).To(Equal(v1beta1.ConnectionStateUnknown))
		Expect(secretExists(t, r.Client, "cpspoke1")).To(BeFalse(), "no connect work while the release is in flight")
	})

	By("the next pass forgetting the infrastructure and connecting", func() {
		_, err := reconcileOnce(t, r, sc)
		Expect(err).NotTo(HaveOccurred())
		latest := readSpoke(t, r, sc)
		Expect(latest.Status.Provisioning).To(BeNil())
		Expect(meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionInfraProvisioned)).To(BeNil())
		wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionTrue, reasonProbeSucceeded)
		Expect(latest.Status.Connection).To(Equal(v1beta1.ConnectionStateConnected))
		Expect(secretExists(t, r.Client, "cpspoke1")).To(BeTrue(), "connect path registered the gateway secret")
	})
})

var _ = It("ReleaseRefusedWithoutRetainRule", func() {
	t := GinkgoT()
	sc := releasingSpoke("cpspoke1")
	r := newTestReconciler(t, sc, unretainedInfraApp(sc))
	wireHealthySpoke(r)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	Expect(infraAppExists(t, r, sc)).To(BeTrue(), "an Application without a retain rule must never be deleted by release")
	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionInfraProvisioned, metav1.ConditionFalse, reasonReleaseRefused)
	Expect(meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionInfraProvisioned).Message).To(ContainSubstring("infraDeletionPolicy retain"))
	Expect(latest.Status.Connection).To(Equal(v1beta1.ConnectionStateUnknown))
	Expect(latest.Status.Provisioning).NotTo(BeNil(), "the projection stays until the release goes through")
	Expect(secretExists(t, r.Client, "cpspoke1")).To(BeFalse())
})

var _ = It("ConnectWithoutProvisioningStatusNeverTouchesApplications", func() {
	t := GinkgoT()
	sc := connectableSpoke("cpspoke1")
	r := newTestReconciler(t, sc, retainedInfraApp(sc))
	wireHealthySpoke(r)

	_, err := reconcileOnce(t, r, sc)
	Expect(err).NotTo(HaveOccurred())

	Expect(infraAppExists(t, r, sc)).To(BeTrue(), "release only runs for a spoke that used to manage infrastructure")
	latest := readSpoke(t, r, sc)
	wantCondition(t, latest, v1beta1.SpokeClusterConditionConnected, metav1.ConditionTrue, reasonProbeSucceeded)
	Expect(meta.FindStatusCondition(latest.Status.Conditions, v1beta1.SpokeClusterConditionInfraProvisioned)).To(BeNil())
})
