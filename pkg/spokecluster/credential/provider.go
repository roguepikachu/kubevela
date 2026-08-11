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

// Package credential resolves hub-to-spoke connectivity for the SpokeCluster
// controller. Each Provider turns a SpokeCluster's declared credential
// (spec.credential, a discriminated union keyed by spec.credential.type) into a
// Materialized connectivity result that the controller writes as a
// cluster-gateway Secret. Providers are side-effect free with respect to that
// Secret: they read the source credential and any cloud APIs, but never write
// the gateway Secret themselves.
//
// # Materialized-to-gateway-Secret contract
//
// The connect step writes the gateway Secret from a Materialized;
// this package fixes the shape so providers and the connect step agree:
//
//   - Name and namespace: named after the SpokeCluster, in
//     multicluster.ClusterGatewaySecretNamespace, of type Opaque. This matches
//     what "vela cluster join" writes, so a SpokeCluster-managed spoke and a
//     manually joined one are indistinguishable to read-through and topology
//     dispatch.
//   - Data keys: "endpoint" always; "ca.crt" when CAData is non-empty; "tls.crt"
//     and "tls.key" when HasClientCert(); "token" otherwise.
//   - Label: the existing cluster-gateway constant
//     clustercommon.LabelKeyClusterCredentialType
//     ("cluster.core.oam.dev/cluster-credential-type"), value "X509Certificate"
//     when HasClientCert() and "ServiceAccountToken" otherwise. No new label is
//     introduced.
//   - Ownership: an owner reference back to the SpokeCluster is set only when the
//     deletion policy is not orphan (spec.deletionPolicy) AND the
//     SpokeCluster shares the gateway Secret namespace. Cross-namespace owner
//     references are forbidden, so the namespace guard is required. The finalizer
//     is the cleanup path that always works; the owner reference is
//     backup GC only where it is valid.
//
// # Source-Secret contract (kubeconfig arm)
//
// The kubeconfig arm resolves a user-owned source Secret from
// spec.credential.kubeconfig.secretRef (a SecretKeyRef: name, optional namespace,
// optional key). The data key defaults to "kubeconfig" via the exported constant
// DefaultKubeconfigSecretKey (declared in kubeconfig.go),
// and an empty namespace falls back to the SpokeCluster's own namespace at resolve
// time. An explicit namespace that differs from the SpokeCluster's is rejected by
// the admission webhook and again here, so a tenant SpokeCluster cannot coerce
// the controller into reading another namespace's Secret. No label of any kind is
// required on the source Secret: the credential type is chosen by
// spec.credential.type, not by a Secret label.
// Structural validation of the union is the webhook's job; providers
// validate the resolved credential material at materialize time. The aws arm has
// no source Secret at all; its provider mints an EKS token from workload identity
// and ignores cli.
//
// # Refresh semantics contract
//
// NextRefresh is the contract between providers and the reconcile loop.
//
// A zero NextRefresh marks a static credential whose source can change with nothing
// to signal it: an opaque kubeconfig token, or a client cert the provider could not
// parse. Nothing is scheduled, the reconcile cadence is the probe interval alone,
// and the controller re-materializes on every pass so a rotated source Secret is
// picked up. Zero therefore means "do not cache", never "cache forever".
//
// A kubeconfig bearer JWT with exp, or a parseable client certificate, is not
// static: the provider sets NextRefresh two minutes before that expiry so a
// projected ServiceAccount token or short-lived cert is rematerialized (and a
// rotated source Secret is re-read) before the previous credential dies. If
// expiry is already inside that lead window but still in the future, NextRefresh
// is the hard expiry itself, so a long probe interval cannot leave a dying
// token in the gateway Secret. If expiry is already past, NextRefresh stays
// zero: the credential is uncacheable and the next pass follows the probe
// interval, rather than forcing a minRequeue busy loop.
//
// A non-zero NextRefresh requires the controller to re-materialize and rewrite the
// gateway Secret no later than that time. Between then and the previous remint the
// controller may reuse the result rather than re-deriving it, which is what keeps an
// aws spoke from spending an sts:AssumeRole and an eks:DescribeCluster on every probe.
//
// Providers set NextRefresh with a renew-before-expiry lead, so a credential is
// reminted while it still works rather than as it stops. The aws provider takes its
// 15 minute presign window and subtracts a 2 minute lead, leaving room for clock skew
// between the hub and AWS and for the gap between minting a token and cluster-gateway
// presenting it.
package credential

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// Materialized is the connectivity a provider resolves for one spoke. It maps
// directly onto the cluster-gateway Secret shape: either a bearer Token or a
// client cert/key pair, plus the endpoint and CA. Exactly one of Token and the
// cert pair is expected to be set; providers must not emit a half pair (see
// HasClientCert).
type Materialized struct {
	// Endpoint is the spoke API server URL.
	Endpoint string
	// CAData is the PEM CA bundle cluster-gateway uses to verify the spoke API
	// server. Kubeconfig materialization requires a non-empty bundle; an empty
	// value is only safe for tests that build Materialized by hand.
	CAData []byte
	// ServerName overrides the hostname the endpoint's certificate is verified
	// against (kubeconfig tls-server-name). Empty means verify against the
	// endpoint host.
	ServerName string
	// Token is a bearer token (aws arm, token-based kubeconfigs).
	Token string
	// ClientCertData is the mTLS client certificate (x509 kubeconfigs).
	ClientCertData []byte
	// ClientKeyData is the mTLS client key paired with ClientCertData.
	ClientKeyData []byte
	// Region is the cloud region, surfaced into status.clusterInfo for the aws
	// arm and empty otherwise.
	Region string
	// NextRefresh is when the credential must be reminted; zero means never. See
	// the refresh semantics contract in the package doc.
	NextRefresh time.Time
}

// HasClientCert reports whether the materialized credential is a complete mTLS
// client cert pair. A half-set pair (cert without key, or key without cert) falls
// through to the token path rather than producing a broken x509 Secret; providers
// must not emit a half pair.
func (m *Materialized) HasClientCert() bool {
	return len(m.ClientCertData) > 0 && len(m.ClientKeyData) > 0
}

// DeepCopy returns a copy that shares no memory with m. The three []byte fields are
// the reason it exists: the connect step aliases CAData, ClientCertData and
// ClientKeyData straight into the gateway Secret rather than copying them, so a
// Materialized that outlives a single reconcile (the controller caches one until its
// refresh deadline) must not stay reachable for mutation from an earlier pass.
//
// Hand-written rather than generated: Materialized is not an API type and carries no
// deepcopy-gen marker, so a new field has to be added here too.
func (m *Materialized) DeepCopy() *Materialized {
	if m == nil {
		return nil
	}
	// Strings and time.Time are values; only the three []byte fields alias.
	out := *m
	out.CAData = bytes.Clone(m.CAData)
	out.ClientCertData = bytes.Clone(m.ClientCertData)
	out.ClientKeyData = bytes.Clone(m.ClientKeyData)
	return &out
}

// Provider resolves connectivity for one SpokeCluster credential type.
type Provider interface {
	// Type returns the credential discriminator this provider handles.
	Type() v1beta1.CredentialType
	// Materialize resolves connectivity for the given SpokeCluster. cli is a
	// hub-side reader used for source Secrets; it must not mutate the gateway
	// Secret. Prefer an uncached reader (mgr.GetAPIReader) so source Gets do not
	// start a cluster-wide Secret informer. When the declared credential is
	// invalid or unresolvable, Materialize returns a descriptive error rather
	// than a partial result.
	Materialize(ctx context.Context, cli client.Reader, sc *v1beta1.SpokeCluster) (*Materialized, error)
}

// Registry maps credential types to their providers.
type Registry map[v1beta1.CredentialType]Provider

// For returns the provider for the given credential type, or an error naming the
// missing type if none is registered. It never panics, so the controller fails
// cleanly on an out-of-enum type that bypassed schema and webhook validation.
func (r Registry) For(t v1beta1.CredentialType) (Provider, error) {
	p, ok := r[t]
	if !ok {
		return nil, fmt.Errorf("no credential provider registered for type %q", t)
	}
	return p, nil
}

// DefaultRegistry returns the built-in provider set and is the single
// registration point for built-in providers. All four credential arms are
// registered: kubeconfig and AWS EKS have a working Materialize, while azure and
// gcp are registered stubs whose Materialize returns a not-implemented error.
// Registering the stubs means a misconfigured azure/gcp SpokeCluster fails with
// an arm-specific "not implemented yet" message instead of a generic "no
// provider registered", and finishing them is a single-file change.
func DefaultRegistry() Registry {
	return Registry{
		v1beta1.CredentialTypeKubeconfig: NewKubeconfigProvider(),
		v1beta1.CredentialTypeAWS:        NewAWSProvider(),
		v1beta1.CredentialTypeAzure:      NewAzureProvider(),
		v1beta1.CredentialTypeGCP:        NewGCPProvider(),
	}
}
