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

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// GCPProvider is a placeholder for connecting to a GKE spoke via GCP
// cloud-native identity. The credential arm (v1beta1.GCPCredential) and its
// schema exist, but materialization is not implemented yet, so Materialize
// returns a not-implemented error rather than a partial result.
//
// TODO: implement GCP hub-to-spoke connectivity. Following the aws arm as the
// reference, this provider should:
//   - authenticate with the ambient hub identity per cred.AuthMode
//     (workloadIdentityFederation: Workload Identity Federation; serviceAccount:
//     impersonation of cred.ServiceAccountEmail),
//   - resolve the GKE cluster (projectID, location, clusterName) to its API
//     server endpoint and CA,
//   - mint a short-lived bearer token and set Materialized.NextRefresh with a
//     renew-before-expiry lead so the token is reminted before it stops working.
type GCPProvider struct{}

// NewGCPProvider builds the GCP provider stub.
func NewGCPProvider() *GCPProvider { return &GCPProvider{} }

// Type returns the gcp credential type.
func (p *GCPProvider) Type() v1beta1.CredentialType { return v1beta1.CredentialTypeGCP }

// Materialize is not implemented. The gcp arm is accepted by the schema so the
// API is forward-compatible, but no connectivity is resolved yet.
func (p *GCPProvider) Materialize(_ context.Context, _ client.Client, _ *v1beta1.SpokeCluster) (*Materialized, error) {
	// TODO: implement GKE connectivity (see the type doc above and aws.go).
	return nil, fmt.Errorf("gcp credential provider is not implemented yet")
}
