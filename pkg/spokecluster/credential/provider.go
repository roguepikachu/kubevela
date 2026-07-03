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

// Package credential resolves hub-to-spoke connectivity for the SpokeCluster controller.
// Each provider turns a SpokeCluster's declared credential into a Materialized result that the
// controller writes into a cluster-gateway secret. Providers are side-effect free: they read the
// source credential and any cloud APIs, but never write the gateway secret themselves.
package credential

import (
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// Materialized is the connectivity a provider resolves for one spoke. It maps directly onto the
// cluster-gateway secret shape: either a bearer Token or a client cert/key pair, plus the
// endpoint and CA. NextRefresh is zero for static credentials and a future time for credentials
// that must be reminted (for example AWS EKS tokens).
type Materialized struct {
	// Endpoint is the spoke API server URL.
	Endpoint string
	// CAData is the PEM CA bundle. Empty means the endpoint is trusted without verification.
	CAData []byte
	// Token is a bearer token. Set for the aws arm and for token-based kubeconfigs.
	Token string
	// ClientCertData and ClientKeyData carry an mTLS client credential (x509 kubeconfigs).
	ClientCertData []byte
	ClientKeyData  []byte
	// Region is the cloud region, surfaced into status.clusterInfo for the aws arm.
	Region string
	// NextRefresh is when the credential must be reminted. Zero means it never expires.
	NextRefresh time.Time
}

// HasClientCert reports whether the materialized credential is an mTLS client cert pair.
func (m *Materialized) HasClientCert() bool {
	return len(m.ClientCertData) > 0 && len(m.ClientKeyData) > 0
}

// Provider resolves connectivity for a SpokeCluster credential type.
type Provider interface {
	// Type returns the credential discriminator this provider handles.
	Type() v1beta1.CredentialType
	// Materialize resolves connectivity for the given SpokeCluster. cli is a hub-side reader used
	// for source secrets. It must not mutate the gateway secret.
	Materialize(ctx context.Context, cli client.Client, sc *v1beta1.SpokeCluster) (*Materialized, error)
}

// Registry maps credential types to their providers.
type Registry map[v1beta1.CredentialType]Provider

// For returns the provider for the given credential type, or an error if none is registered.
func (r Registry) For(t v1beta1.CredentialType) (Provider, error) {
	p, ok := r[t]
	if !ok {
		return nil, fmt.Errorf("no credential provider registered for type %q", t)
	}
	return p, nil
}

// DefaultRegistry returns the built-in provider set: static kubeconfig and AWS EKS.
func DefaultRegistry() Registry {
	return Registry{
		v1beta1.CredentialTypeKubeconfig: NewKubeconfigProvider(),
		v1beta1.CredentialTypeAWS:        NewAWSProvider(),
	}
}
