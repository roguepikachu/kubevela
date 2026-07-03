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

package e2e_multicluster_test

import (
	"context"
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// These specs exercise SpokeCluster connect end to end against the worker cluster. They require
// vela-cluster-core running with the EnableSpokeClusterCRD feature; when the CRD is absent (the
// feature is off) the specs skip, so the default multicluster suite stays green.
var _ = Describe("Test SpokeCluster connect", func() {
	const (
		spokeName    = "e2e-spoke"
		spokeNS      = "vela-system"
		kubeconfigKC = "e2e-spoke-kubeconfig"
	)

	BeforeEach(func() {
		// Skip unless the SpokeCluster CRD is installed (vela-cluster-core + feature enabled).
		list := &v1beta1.SpokeClusterList{}
		if err := k8sClient.List(context.Background(), list); err != nil {
			Skip("SpokeCluster CRD not installed; skipping connect e2e: " + err.Error())
		}
	})

	AfterEach(func() {
		ctx := context.Background()
		_ = k8sClient.Delete(ctx, &v1beta1.SpokeCluster{ObjectMeta: metav1.ObjectMeta{Name: spokeName, Namespace: spokeNS}})
		_ = k8sClient.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: kubeconfigKC, Namespace: spokeNS}})
	})

	It("connects to the worker cluster via a kubeconfig credential and reports status", func() {
		raw, err := os.ReadFile(WorkerClusterKubeConfigPath)
		Expect(err).Should(Succeed())
		ctx := context.Background()

		By("creating the source kubeconfig secret on the hub")
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: kubeconfigKC, Namespace: spokeNS},
			Data:       map[string][]byte{"kubeconfig": raw},
		}
		Expect(k8sClient.Create(ctx, secret)).Should(Succeed())

		By("creating the SpokeCluster in connect mode")
		sc := &v1beta1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: spokeName, Namespace: spokeNS},
			Spec: v1beta1.SpokeClusterSpec{
				Mode: v1beta1.SpokeClusterModeConnect,
				Credential: v1beta1.CredentialSpec{
					Type:       v1beta1.CredentialTypeKubeconfig,
					Kubeconfig: &v1beta1.KubeconfigCredential{SecretRef: v1beta1.SecretKeyRef{Name: kubeconfigKC, Namespace: spokeNS}},
				},
			},
		}
		Expect(k8sClient.Create(ctx, sc)).Should(Succeed())

		By("waiting for the SpokeCluster to report Connected with discovered info")
		Eventually(func(g Gomega) {
			got := &v1beta1.SpokeCluster{}
			g.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: spokeName, Namespace: spokeNS}, got)).Should(Succeed())
			g.Expect(got.Status.Connection).Should(Equal(v1beta1.ConnectionStateConnected))
			g.Expect(got.Status.ClusterInfo).ShouldNot(BeNil())
			g.Expect(got.Status.ClusterInfo.KubernetesVersion).ShouldNot(BeEmpty())
			g.Expect(got.Status.ClusterInfo.NodeCount).Should(BeNumerically(">", 0))
		}, 2*time.Minute, 5*time.Second).Should(Succeed())

		By("confirming the cluster-gateway secret was materialized with an owner reference")
		gwSecret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: spokeName, Namespace: spokeNS}, gwSecret)).Should(Succeed())
		Expect(gwSecret.Labels).Should(HaveKey("cluster.core.oam.dev/cluster-credential-type"))
		Expect(gwSecret.OwnerReferences).ShouldNot(BeEmpty())

		By("deleting the SpokeCluster and confirming detach removes the gateway secret")
		Expect(k8sClient.Delete(ctx, sc)).Should(Succeed())
		Eventually(func(g Gomega) {
			err := k8sClient.Get(ctx, apitypes.NamespacedName{Name: spokeName, Namespace: spokeNS}, &corev1.Secret{})
			g.Expect(client.IgnoreNotFound(err)).Should(Succeed())
			g.Expect(err).ShouldNot(BeNil()) // NotFound
		}, time.Minute, 5*time.Second).Should(Succeed())
	})
})
