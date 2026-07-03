/*
 Copyright 2026. The KubeVela Authors.

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

package v1beta1_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// These tests exercise the SpokeCluster CRD's schema-level admission (enum, required, defaults)
// against a real apiserver via envtest. They are skipped when KUBEBUILDER_ASSETS is not set,
// so `go test` without envtest assets stays green.
func TestSpokeClusterAdmission(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; skipping envtest admission suite")
	}

	testScheme := runtime.NewScheme()
	if err := scheme.AddToScheme(testScheme); err != nil {
		t.Fatalf("add client-go scheme: %v", err)
	}
	if err := v1beta1.SchemeBuilder.AddToScheme(testScheme); err != nil {
		t.Fatalf("add v1beta1 scheme: %v", err)
	}

	testEnv := &envtest.Environment{
		ControlPlaneStartTimeout: time.Minute,
		ControlPlaneStopTimeout:  time.Minute,
		CRDDirectoryPaths:        []string{filepath.Join("..", "..", "..", "charts", "vela-core", "crds")},
	}
	var cfg *rest.Config
	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		t.Fatalf("start envtest: %v", err)
	}
	defer func() { _ = testEnv.Stop() }()

	k8sClient, err := client.New(cfg, client.Options{Scheme: testScheme})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	ctx := context.Background()
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "vela-system"}}
	if err := k8sClient.Create(ctx, ns); err != nil {
		t.Fatalf("create namespace: %v", err)
	}

	t.Run("accepts a valid kubeconfig SpokeCluster and applies defaults", func(t *testing.T) {
		sc := &v1beta1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "valid-kc", Namespace: "vela-system"},
			Spec: v1beta1.SpokeClusterSpec{
				Credential: v1beta1.CredentialSpec{
					Type:       v1beta1.CredentialTypeKubeconfig,
					Kubeconfig: &v1beta1.KubeconfigCredential{SecretRef: v1beta1.SecretKeyRef{Name: "kc"}},
				},
			},
		}
		if err := k8sClient.Create(ctx, sc); err != nil {
			t.Fatalf("expected create to succeed: %v", err)
		}
		got := &v1beta1.SpokeCluster{}
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(sc), got); err != nil {
			t.Fatalf("get: %v", err)
		}
		// CRD-level defaults must be applied by the apiserver.
		if got.Spec.Mode != v1beta1.SpokeClusterModeConnect {
			t.Errorf("mode default = %q, want connect", got.Spec.Mode)
		}
		if got.Spec.ProbeIntervalSeconds != 30 {
			t.Errorf("probeIntervalSeconds default = %d, want 30", got.Spec.ProbeIntervalSeconds)
		}
		if got.Spec.DeletionPolicy != v1beta1.SpokeDeletionPolicyDetach {
			t.Errorf("deletionPolicy default = %q, want detach", got.Spec.DeletionPolicy)
		}
	})

	t.Run("rejects an invalid credential type", func(t *testing.T) {
		sc := &v1beta1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-type", Namespace: "vela-system"},
			Spec: v1beta1.SpokeClusterSpec{
				Credential: v1beta1.CredentialSpec{Type: v1beta1.CredentialType("gcp")},
			},
		}
		if err := k8sClient.Create(ctx, sc); err == nil {
			t.Fatal("expected apiserver to reject an out-of-enum credential type")
		}
	})

	t.Run("rejects an out-of-range probe interval", func(t *testing.T) {
		sc := &v1beta1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-probe", Namespace: "vela-system"},
			Spec: v1beta1.SpokeClusterSpec{
				ProbeIntervalSeconds: 5, // below the minimum of 10
				Credential: v1beta1.CredentialSpec{
					Type:       v1beta1.CredentialTypeKubeconfig,
					Kubeconfig: &v1beta1.KubeconfigCredential{SecretRef: v1beta1.SecretKeyRef{Name: "kc"}},
				},
			},
		}
		if err := k8sClient.Create(ctx, sc); err == nil {
			t.Fatal("expected apiserver to reject probeIntervalSeconds below the minimum")
		}
	})

	t.Run("rejects an invalid aws authMode", func(t *testing.T) {
		sc := &v1beta1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-authmode", Namespace: "vela-system"},
			Spec: v1beta1.SpokeClusterSpec{
				Credential: v1beta1.CredentialSpec{
					Type: v1beta1.CredentialTypeAWS,
					AWS: &v1beta1.AWSCredential{
						AuthMode:    v1beta1.AWSAuthMode("workloadIdentity"),
						ClusterName: "prod",
						Region:      "us-east-1",
						RoleARN:     "arn:aws:iam::123:role/x",
					},
				},
			},
		}
		if err := k8sClient.Create(ctx, sc); err == nil {
			t.Fatal("expected apiserver to reject an out-of-enum aws authMode")
		}
	})
}
