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

package credential

import (
	"context"
	"strings"

	. "github.com/onsi/ginkgo/v2"
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

const execKubeconfig = `apiVersion: v1
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

const filePathCAKubeconfig = `apiVersion: v1
kind: Config
current-context: spoke
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
    certificate-authority: /etc/ca/spoke.crt
users:
- name: spoke
  user:
    token: tok
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`

const invalidYAMLKubeconfig = `not: valid: yaml: [structure`

const noCurrentContextKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
users:
- name: spoke
  user:
    token: tok
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`

const danglingContextKubeconfig = `apiVersion: v1
kind: Config
current-context: ghost
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
users:
- name: spoke
  user:
    token: tok
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`

const unknownClusterKubeconfig = `apiVersion: v1
kind: Config
current-context: spoke
clusters:
- name: other
  cluster:
    server: https://other.example.com:6443
users:
- name: spoke
  user:
    token: tok
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`

const unknownUserKubeconfig = `apiVersion: v1
kind: Config
current-context: spoke
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
users:
- name: other
  user:
    token: tok
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`

func newFakeClient(t GinkgoTInterface, objs ...runtime.Object) *fake.ClientBuilder {
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

var _ = It("KubeconfigProviderToken", func() {
	t := GinkgoT()
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
})

var _ = It("KubeconfigProviderClientCert", func() {
	t := GinkgoT()
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
	if !m.NextRefresh.IsZero() {
		t.Fatal("static kubeconfig should not schedule a refresh")
	}
})

var _ = It("KubeconfigProviderResolutionErrors", func() {
	t := GinkgoT()
	baseSC := func() *v1beta1.SpokeCluster {
		return &v1beta1.SpokeCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "spoke", Namespace: "vela-system"},
			Spec: v1beta1.SpokeClusterSpec{
				Credential: v1beta1.CredentialSpec{
					Type:       v1beta1.CredentialTypeKubeconfig,
					Kubeconfig: &v1beta1.KubeconfigCredential{SecretRef: v1beta1.SecretKeyRef{Name: "spoke-kc"}},
				},
			},
		}
	}
	nilArmSC := &v1beta1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke", Namespace: "vela-system"},
		Spec:       v1beta1.SpokeClusterSpec{Credential: v1beta1.CredentialSpec{Type: v1beta1.CredentialTypeKubeconfig}},
	}

	cases := map[string]struct {
		sc          *v1beta1.SpokeCluster
		secret      *corev1.Secret
		wantErrText string
	}{
		"nil kubeconfig arm": {
			sc:          nilArmSC,
			wantErrText: "credential.kubeconfig is required when type is kubeconfig",
		},
		"secret missing": {
			sc:          baseSC(),
			wantErrText: "failed to read kubeconfig secret vela-system/spoke-kc",
		},
		"key missing": {
			sc:          baseSC(),
			secret:      kubeconfigSecret("vela-system", "spoke-kc", "other-key", tokenKubeconfig),
			wantErrText: `kubeconfig secret vela-system/spoke-kc has no data at key "kubeconfig"`,
		},
		"key value empty": {
			sc:          baseSC(),
			secret:      kubeconfigSecret("vela-system", "spoke-kc", DefaultKubeconfigSecretKey, ""),
			wantErrText: `kubeconfig secret vela-system/spoke-kc has no data at key "kubeconfig"`,
		},
	}

	for name, tc := range cases {
		By(name, func() {
			builder := newFakeClient(t)
			if tc.secret != nil {
				builder = builder.WithObjects(tc.secret)
			}
			cli := builder.Build()
			_, err := NewKubeconfigProvider().Materialize(context.Background(), cli, tc.sc)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErrText) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErrText)
			}
		})
	}
})

var _ = It("MaterializeFromKubeconfigParseErrors", func() {
	t := GinkgoT()
	cases := map[string]struct {
		kubeconfig  string
		wantErrText string
	}{
		"invalid yaml": {
			kubeconfig:  invalidYAMLKubeconfig,
			wantErrText: "failed to parse kubeconfig",
		},
		"current-context unset": {
			kubeconfig:  noCurrentContextKubeconfig,
			wantErrText: `kubeconfig has no current-context ""`,
		},
		"current-context dangling": {
			kubeconfig:  danglingContextKubeconfig,
			wantErrText: `kubeconfig has no current-context "ghost"`,
		},
		"unknown cluster": {
			kubeconfig:  unknownClusterKubeconfig,
			wantErrText: `kubeconfig references unknown cluster "spoke"`,
		},
		"unknown user": {
			kubeconfig:  unknownUserKubeconfig,
			wantErrText: `kubeconfig references unknown user "spoke"`,
		},
		"exec credentials unsupported": {
			kubeconfig:  execKubeconfig,
			wantErrText: "exec and file-path credentials are not supported",
		},
		"file-path certificate-authority rejected": {
			kubeconfig:  filePathCAKubeconfig,
			wantErrText: "only inline certificate-authority-data is supported",
		},
	}
	for name, tc := range cases {
		By(name, func() {
			_, err := materializeFromKubeconfig([]byte(tc.kubeconfig))
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErrText) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.wantErrText)
			}
		})
	}
})

var _ = It("MaterializeFromKubeconfigPreservesServerName", func() {
	t := GinkgoT()
	// tls-server-name is a plain string (no file read) and must be carried into
	// the materialized credential so TLS verification uses the intended name.
	serverNameKubeconfig := `apiVersion: v1
kind: Config
current-context: spoke
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
    certificate-authority-data: Y2FkYXRh
    tls-server-name: api.internal.spoke
users:
- name: spoke
  user:
    token: tok
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`
	m, err := materializeFromKubeconfig([]byte(serverNameKubeconfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.ServerName != "api.internal.spoke" {
		t.Fatalf("ServerName = %q, want api.internal.spoke", m.ServerName)
	}
})

var _ = It("MaterializeFromKubeconfigInsecureSkipTLSVerify", func() {
	t := GinkgoT()
	// insecure-skip-tls-verify must leave CAData empty even when the kubeconfig
	// also carries certificate-authority-data: verification is skipped entirely,
	// so there is no CA bundle to carry forward.
	insecureSkipTLSKubeconfig := `apiVersion: v1
kind: Config
current-context: spoke
clusters:
- name: spoke
  cluster:
    server: https://spoke.example.com:6443
    certificate-authority-data: Y2FkYXRh
    insecure-skip-tls-verify: true
users:
- name: spoke
  user:
    token: tok
contexts:
- name: spoke
  context:
    cluster: spoke
    user: spoke
`
	m, err := materializeFromKubeconfig([]byte(insecureSkipTLSKubeconfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(m.CAData) != 0 {
		t.Fatalf("CAData = %q, want empty when insecure-skip-tls-verify is set", string(m.CAData))
	}
})

var _ = It("KubeconfigProviderExplicitNamespace", func() {
	t := GinkgoT()
	// secretRef.namespace, when set explicitly, is read as given even though it
	// differs from the SpokeCluster's own namespace. This complements the
	// existing empty-namespace tests, which only exercise the same-namespace
	// fallback because the Secret happens to sit in "vela-system" either way.
	secret := kubeconfigSecret("other-ns", "spoke-kc", DefaultKubeconfigSecretKey, tokenKubeconfig)
	cli := newFakeClient(t).WithObjects(secret).Build()
	sc := &v1beta1.SpokeCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "spoke", Namespace: "vela-system"},
		Spec: v1beta1.SpokeClusterSpec{
			Credential: v1beta1.CredentialSpec{
				Type: v1beta1.CredentialTypeKubeconfig,
				Kubeconfig: &v1beta1.KubeconfigCredential{
					SecretRef: v1beta1.SecretKeyRef{Name: "spoke-kc", Namespace: "other-ns"},
				},
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
})
