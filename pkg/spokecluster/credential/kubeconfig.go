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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// DefaultKubeconfigSecretKey is the Secret data key used when the credential does not name one.
const DefaultKubeconfigSecretKey = "kubeconfig"

// KubeconfigProvider resolves connectivity from a static kubeconfig held in a hub Secret.
// It performs no refresh: the resolved credential lives as long as the source kubeconfig does.
type KubeconfigProvider struct{}

// NewKubeconfigProvider builds a static kubeconfig provider.
func NewKubeconfigProvider() *KubeconfigProvider { return &KubeconfigProvider{} }

// Type returns the kubeconfig credential type.
func (p *KubeconfigProvider) Type() v1beta1.CredentialType { return v1beta1.CredentialTypeKubeconfig }

// Materialize reads the referenced Secret, parses the kubeconfig, and extracts the current
// context's endpoint, CA, and auth (bearer token or client cert/key).
func (p *KubeconfigProvider) Materialize(ctx context.Context, cli client.Client, sc *v1beta1.SpokeCluster) (*Materialized, error) {
	if sc.Spec.Credential.Kubeconfig == nil {
		return nil, fmt.Errorf("credential.kubeconfig is required when type is kubeconfig")
	}
	ref := sc.Spec.Credential.Kubeconfig.SecretRef
	ns := ref.Namespace
	if ns == "" {
		ns = sc.Namespace
	} else if ns != sc.Namespace {
		// Defense in depth for the webhook's same-namespace policy: a SpokeCluster
		// admitted before that check existed, or with the webhook disabled, must
		// still not be able to coerce a cross-namespace Secret read.
		return nil, fmt.Errorf("cross-namespace kubeconfig secretRef is not permitted (SpokeCluster is in %q, secretRef.namespace is %q)", sc.Namespace, ns)
	}
	key := ref.Key
	if key == "" {
		key = DefaultKubeconfigSecretKey
	}

	secret := &corev1.Secret{}
	if err := cli.Get(ctx, apitypes.NamespacedName{Name: ref.Name, Namespace: ns}, secret); err != nil {
		return nil, fmt.Errorf("failed to read kubeconfig secret %s/%s: %w", ns, ref.Name, err)
	}
	raw, ok := secret.Data[key]
	if !ok || len(raw) == 0 {
		return nil, fmt.Errorf("kubeconfig secret %s/%s has no data at key %q", ns, ref.Name, key)
	}
	return materializeFromKubeconfig(raw)
}

// materializeFromKubeconfig parses raw kubeconfig bytes into a Materialized credential using the
// kubeconfig's current context.
func materializeFromKubeconfig(raw []byte) (*Materialized, error) {
	cfg, err := clientcmd.Load(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse kubeconfig: %w", err)
	}
	ctxName := cfg.CurrentContext
	kubeCtx, ok := cfg.Contexts[ctxName]
	if !ok {
		return nil, fmt.Errorf("kubeconfig has no current-context %q", ctxName)
	}
	cluster, ok := cfg.Clusters[kubeCtx.Cluster]
	if !ok {
		return nil, fmt.Errorf("kubeconfig references unknown cluster %q", kubeCtx.Cluster)
	}
	authInfo, ok := cfg.AuthInfos[kubeCtx.AuthInfo]
	if !ok {
		return nil, fmt.Errorf("kubeconfig references unknown user %q", kubeCtx.AuthInfo)
	}

	// A file-path certificate-authority cannot be honored here: the kubeconfig
	// arrives as Secret data, so the path refers to the machine that produced the
	// kubeconfig, not the hub controller's filesystem, and reading a
	// Secret-supplied path would be an untrusted file read. Reject it rather than
	// silently dropping the trust anchor (which would leave CAData empty and skip
	// verification). This matches how exec and file-path auth credentials are
	// rejected below; connect requires inline certificate-authority-data.
	if cluster.CertificateAuthority != "" {
		return nil, fmt.Errorf("kubeconfig cluster %q uses a file-path certificate-authority; only inline certificate-authority-data is supported for connect", kubeCtx.Cluster)
	}

	if err := ValidateSpokeEndpoint(cluster.Server); err != nil {
		return nil, err
	}

	m := &Materialized{
		Endpoint:   cluster.Server,
		ServerName: cluster.TLSServerName,
	}
	if !cluster.InsecureSkipTLSVerify {
		m.CAData = cluster.CertificateAuthorityData
	}

	switch {
	case authInfo.Token != "":
		m.Token = authInfo.Token
	case len(authInfo.ClientCertificateData) > 0 && len(authInfo.ClientKeyData) > 0:
		m.ClientCertData = authInfo.ClientCertificateData
		m.ClientKeyData = authInfo.ClientKeyData
	default:
		return nil, fmt.Errorf("kubeconfig user %q has no embedded token or client cert/key; exec and file-path credentials are not supported for connect", kubeCtx.AuthInfo)
	}
	return m, nil
}
