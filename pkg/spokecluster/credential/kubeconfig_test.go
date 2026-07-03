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

package credential

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

const tokenKubeconfig = `apiVersion: v1
kind: Config
current-context: spoke
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
    certificate-authority-data: Y2FkYXRh
users:
- name: spoke
  user:
    token: super-secret-token
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`

const certKubeconfig = `apiVersion: v1
kind: Config
current-context: spoke
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
    certificate-authority-data: Y2FkYXRh
users:
- name: spoke
  user:
    client-certificate-data: Y2VydA==
    client-key-data: a2V5
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`

func newFakeClient(t *testing.T, objs ...runtime.Object) *fake.ClientBuilder {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme)
}

func kubeconfigSecret(ns, name, key, data string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Data:       map[string][]byte{key: []byte(data)},
	}
}

func TestKubeconfigProviderToken(t *testing.T) {
	secret := kubeconfigSecret("vela-system", "spoke-kc", DefaultKubeconfigSecretKey, tokenKubeconfig)
	cli := newFakeClient(t).WithObjects(secret).Build()
	sc := &v1beta1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke", Namespace: "vela-system"},
		Spec: v1beta1.SpokeClusterSpec{
			Credential: v1beta1.CredentialSpec{
				Type:       v1beta1.CredentialTypeKubeconfig,
				Kubeconfig: &v1beta1.KubeconfigCredential{SecretRef: v1beta1.SecretKeyRef{Name: "spoke-kc"}},
			},
		},
	}
	m, err := NewKubeconfigProvider().Materialize(context.Background(), cli, sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Endpoint != "https://spoke.example.com:6443" {
		t.Fatalf("endpoint = %q", m.Endpoint)
	}
	if m.Token != "super-secret-token" {
		t.Fatalf("token = %q", m.Token)
	}
	if string(m.CAData) != "cadata" {
		t.Fatalf("ca = %q", string(m.CAData))
	}
	if m.HasClientCert() {
		t.Fatal("token kubeconfig should not carry a client cert")
	}
	if !m.NextRefresh.IsZero() {
		t.Fatal("static kubeconfig should not schedule a refresh")
	}
}

func TestKubeconfigProviderClientCert(t *testing.T) {
	secret := kubeconfigSecret("vela-system", "spoke-kc", DefaultKubeconfigSecretKey, certKubeconfig)
	cli := newFakeClient(t).WithObjects(secret).Build()
	sc := &v1beta1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke", Namespace: "vela-system"},
		Spec: v1beta1.SpokeClusterSpec{
			Credential: v1beta1.CredentialSpec{
				Type:       v1beta1.CredentialTypeKubeconfig,
				Kubeconfig: &v1beta1.KubeconfigCredential{SecretRef: v1beta1.SecretKeyRef{Name: "spoke-kc"}},
			},
		},
	}
	m, err := NewKubeconfigProvider().Materialize(context.Background(), cli, sc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !m.HasClientCert() {
		t.Fatal("cert kubeconfig should carry a client cert pair")
	}
	if m.Token != "" {
		t.Fatalf("cert kubeconfig should not carry a token, got %q", m.Token)
	}
}

func TestKubeconfigProviderErrors(t *testing.T) {
	sc := &v1beta1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke", Namespace: "vela-system"},
		Spec: v1beta1.SpokeClusterSpec{
			Credential: v1beta1.CredentialSpec{
				Type:       v1beta1.CredentialTypeKubeconfig,
				Kubeconfig: &v1beta1.KubeconfigCredential{SecretRef: v1beta1.SecretKeyRef{Name: "missing"}},
			},
		},
	}
	cli := newFakeClient(t).Build()
	if _, err := NewKubeconfigProvider().Materialize(context.Background(), cli, sc); err == nil {
		t.Fatal("expected error when secret is missing")
	}

	// Missing kubeconfig arm.
	scNoArm := &v1beta1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke", Namespace: "vela-system"},
		Spec:       v1beta1.SpokeClusterSpec{Credential: v1beta1.CredentialSpec{Type: v1beta1.CredentialTypeKubeconfig}},
	}
	if _, err := NewKubeconfigProvider().Materialize(context.Background(), cli, scNoArm); err == nil {
		t.Fatal("expected error when kubeconfig arm is nil")
	}
}

func TestMaterializeFromKubeconfigExecUnsupported(t *testing.T) {
	execKubeconfig := `apiVersion: v1
kind: Config
current-context: spoke
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
users:
- name: spoke
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1beta1
      command: aws
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`
	if _, err := materializeFromKubeconfig([]byte(execKubeconfig)); err == nil {
		t.Fatal("expected exec credentials to be rejected for connect")
	}
}
