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

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// admissionTestNamespace holds every SpokeCluster this suite creates.
const admissionTestNamespace = "spokecluster-admission"

// TestSpokeClusterAdmission verifies the CRD's schema-level admission (enum,
// required fields, defaults, bounds) against a real apiserver, with no
// webhook running (Requirement 7). It skips when envtest assets are not
// installed, so plain `go test` stays green (Requirement 7, criterion 5).
func TestSpokeClusterAdmission(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; skipping envtest schema-admission suite")
	}
	r := require.New(t)

	testEnv := &envtest.Environment{
		ControlPlaneStartTimeout: 2 * time.Minute,
		ControlPlaneStopTimeout:  time.Minute,
		UseExistingCluster:       ptr.To(false),
		CRDDirectoryPaths:        []string{spokeClusterCRDPath},
	}

	cfg, err := testEnv.Start()
	r.NoError(err, "envtest environment must start (requires KUBEBUILDER_ASSETS)")
	t.Cleanup(func() {
		r.NoError(testEnv.Stop())
	})

	k8sClient, err := client.New(cfg, client.Options{Scheme: k8sscheme.Scheme})
	r.NoError(err)

	ctx := context.Background()
	r.NoError(k8sClient.Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: admissionTestNamespace},
	}))

	// Requirement 7, criterion 1: a valid kubeconfig SpokeCluster is admitted
	// and the apiserver applies the CRD-level defaults.
	t.Run("ValidKubeconfig_AppliesCRDDefaults", func(t *testing.T) {
		r := require.New(t)

		// Built as Unstructured rather than the typed SpokeCluster struct:
		// Spec.Mode has no `omitempty` json tag, so a typed zero-value Mode
		// would serialize as an explicit "mode":"" -- the apiserver treats
		// the field as present (and rejects it against the enum) instead of
		// filling in the default. Omitting the keys entirely is the only way
		// to exercise the defaulting this test asserts.
		spoke := &unstructured.Unstructured{Object: map[string]interface{}{
			"apiVersion": SchemeGroupVersion.String(),
			"kind":       "SpokeCluster",
			"metadata": map[string]interface{}{
				"name":      "defaults-applied",
				"namespace": admissionTestNamespace,
			},
			"spec": map[string]interface{}{
				"credential": map[string]interface{}{
					"type": string(CredentialTypeKubeconfig),
					"kubeconfig": map[string]interface{}{
						"secretRef": map[string]interface{}{
							"name": "defaults-applied-kubeconfig",
						},
					},
				},
			},
		}}
		r.NoError(k8sClient.Create(ctx, spoke))

		got := &SpokeCluster{}
		r.NoError(k8sClient.Get(ctx, types.NamespacedName{Name: "defaults-applied", Namespace: admissionTestNamespace}, got))
		r.Equal(SpokeClusterModeConnect, got.Spec.Mode)
		r.Equal(int32(30), got.Spec.ProbeIntervalSeconds)
		r.Equal(SpokeDeletionPolicyDetach, got.Spec.DeletionPolicy)
	})

	// Requirement 7, criterion 2: a credential type outside the schema enum
	// is rejected. "oracle" is used rather than azure/gcp, which ARE valid
	// enum values -- their Phase 1 rejection is the webhook's job
	// (Requirement 2, criterion 4), not the schema's.
	t.Run("UnknownCredentialType_Rejected", func(t *testing.T) {
		r := require.New(t)

		spoke := &SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "unknown-credential-type", Namespace: admissionTestNamespace},
			Spec: SpokeClusterSpec{
				Mode: SpokeClusterModeConnect,
				Credential: CredentialSpec{
					Type: CredentialType("oracle"),
				},
			},
		}
		r.Error(k8sClient.Create(ctx, spoke))
	})

	// Requirement 7, criterion 3: probeIntervalSeconds below the schema
	// minimum of 10 is rejected.
	t.Run("ProbeIntervalBelowMinimum_Rejected", func(t *testing.T) {
		r := require.New(t)

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
		r.Error(k8sClient.Create(ctx, spoke))
	})

	// Requirement 7, criterion 4: an out-of-enum aws.authMode is rejected.
	t.Run("UnknownAWSAuthMode_Rejected", func(t *testing.T) {
		r := require.New(t)

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
		r.Error(k8sClient.Create(ctx, spoke))
	})
}
