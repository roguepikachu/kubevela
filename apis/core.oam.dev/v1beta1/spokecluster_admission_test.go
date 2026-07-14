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

package v1beta1

import (
	"context"
	"os"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// admissionTestNamespace holds every SpokeCluster this suite creates.
const admissionTestNamespace = "spokecluster-admission"

var (
	admissionTestEnv   *envtest.Environment
	admissionK8sClient client.Client
)

// TestSpokeClusterAdmission verifies the CRD's schema-level admission (enum,
// required fields, defaults, bounds) against a real apiserver, with no
// webhook running (Requirement 7). It skips when envtest assets are not
// installed, so plain `go test` stays green (Requirement 7, criterion 5).
func TestSpokeClusterAdmission(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SpokeCluster Admission Suite")
}

var _ = BeforeSuite(func() {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		Skip("KUBEBUILDER_ASSETS not set; skipping envtest schema-admission suite")
	}

	admissionTestEnv = &envtest.Environment{
		ControlPlaneStartTimeout: 2 * time.Minute,
		ControlPlaneStopTimeout:  time.Minute,
		UseExistingCluster:       ptr.To(false),
		CRDDirectoryPaths:        []string{spokeClusterCRDPath},
	}

	cfg, err := admissionTestEnv.Start()
	Expect(err).NotTo(HaveOccurred(), "envtest environment must start (requires KUBEBUILDER_ASSETS)")

	admissionK8sClient, err = client.New(cfg, client.Options{Scheme: k8sscheme.Scheme})
	Expect(err).NotTo(HaveOccurred())

	Expect(admissionK8sClient.Create(context.Background(), &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: admissionTestNamespace},
	})).To(Succeed())
})

var _ = AfterSuite(func() {
	if admissionTestEnv != nil {
		Expect(admissionTestEnv.Stop()).To(Succeed())
	}
})

var _ = Describe("SpokeCluster schema admission", func() {
	// Requirement 7, criterion 1: a valid kubeconfig SpokeCluster is admitted
	// and the apiserver applies the CRD-level defaults.
	It("admits a valid kubeconfig spoke and applies CRD defaults", func() {
		ctx := context.Background()

		spoke := &SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "defaults-applied", Namespace: admissionTestNamespace},
			Spec: SpokeClusterSpec{
				Credential: CredentialSpec{
					Type: CredentialTypeKubeconfig,
					Kubeconfig: &KubeconfigCredential{
						SecretRef: SecretKeyRef{Name: "defaults-applied-kubeconfig"},
					},
				},
			},
		}
		Expect(admissionK8sClient.Create(ctx, spoke)).To(Succeed())

		got := &SpokeCluster{}
		Expect(admissionK8sClient.Get(ctx, types.NamespacedName{Name: "defaults-applied", Namespace: admissionTestNamespace}, got)).To(Succeed())
		Expect(got.Spec.Mode).To(Equal(SpokeClusterModeConnect))
		Expect(got.Spec.ProbeIntervalSeconds).To(Equal(int32(30)))
		Expect(got.Spec.DeletionPolicy).To(Equal(SpokeDeletionPolicyDetach))
	})

	// Requirement 7, criterion 2: a credential type outside the schema enum
	// is rejected. "oracle" is used rather than azure/gcp, which ARE valid
	// enum values -- their Phase 1 rejection is the webhook's job
	// (Requirement 2, criterion 4), not the schema's.
	It("rejects a credential type outside the schema enum", func() {
		spoke := &SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "unknown-credential-type", Namespace: admissionTestNamespace},
			Spec: SpokeClusterSpec{
				Mode: SpokeClusterModeConnect,
				Credential: CredentialSpec{
					Type: CredentialType("oracle"),
				},
			},
		}
		Expect(admissionK8sClient.Create(context.Background(), spoke)).To(HaveOccurred())
	})

	// Requirement 7, criterion 3: probeIntervalSeconds below the schema
	// minimum of 10 is rejected.
	It("rejects probeIntervalSeconds below the schema minimum", func() {
		spoke := &SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "probe-interval-too-low", Namespace: admissionTestNamespace},
			Spec: SpokeClusterSpec{
				Mode: SpokeClusterModeConnect,
				Credential: CredentialSpec{
					Type: CredentialTypeKubeconfig,
					Kubeconfig: &KubeconfigCredential{
						SecretRef: SecretKeyRef{Name: "probe-interval-too-low-kubeconfig"},
					},
				},
				ProbeIntervalSeconds: 5, // schema minimum is 10
			},
		}
		Expect(admissionK8sClient.Create(context.Background(), spoke)).To(HaveOccurred())
	})

	// Requirement 7, criterion 4: an out-of-enum aws.authMode is rejected.
	It("rejects an out-of-enum aws.authMode", func() {
		spoke := &SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "unknown-aws-auth-mode", Namespace: admissionTestNamespace},
			Spec: SpokeClusterSpec{
				Mode: SpokeClusterModeConnect,
				Credential: CredentialSpec{
					Type: CredentialTypeAWS,
					AWS: &AWSCredential{
						AuthMode:    AWSAuthMode("workloadIdentity"), // outside the enum (podIdentity, irsa)
						ClusterName: "prod-us-east-1",
						Region:      "us-east-1",
						RoleARN:     "arn:aws:iam::123456789012:role/per-cluster-role",
					},
				},
			},
		}
		Expect(admissionK8sClient.Create(context.Background(), spoke)).To(HaveOccurred())
	})
})
