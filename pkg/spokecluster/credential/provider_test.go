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
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// fakeProvider is a minimal Provider used to exercise the registry and the
// DefaultRegistry invariant without any concrete cloud backend.
type fakeProvider struct {
	credType v1beta1.CredentialType
}

func (f fakeProvider) Type() v1beta1.CredentialType { return f.credType }

func (f fakeProvider) Materialize(_ context.Context, _ client.Client, _ *v1beta1.SpokeCluster) (*Materialized, error) {
	return &Materialized{}, nil
}

func TestHasClientCert(t *testing.T) {
	cases := map[string]struct {
		m    Materialized
		want bool
	}{
		"token only":       {m: Materialized{Token: "t"}, want: false},
		"cert without key": {m: Materialized{ClientCertData: []byte("cert")}, want: false},
		"key without cert": {m: Materialized{ClientKeyData: []byte("key")}, want: false},
		"full pair":        {m: Materialized{ClientCertData: []byte("cert"), ClientKeyData: []byte("key")}, want: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, tc.m.HasClientCert())
		})
	}
}

func TestRegistryFor(t *testing.T) {
	provider := fakeProvider{credType: v1beta1.CredentialTypeKubeconfig}
	reg := Registry{v1beta1.CredentialTypeKubeconfig: provider}

	t.Run("registered type returns its provider", func(t *testing.T) {
		got, err := reg.For(v1beta1.CredentialTypeKubeconfig)
		require.NoError(t, err)
		require.Equal(t, v1beta1.CredentialTypeKubeconfig, got.Type())
	})

	t.Run("unregistered type returns a descriptive error and does not panic", func(t *testing.T) {
		require.NotPanics(t, func() {
			got, err := reg.For(v1beta1.CredentialType("unknown"))
			require.Nil(t, got)
			require.Error(t, err)
			require.Contains(t, err.Error(), "unknown")
		})
	})
}

func TestDefaultRegistryKeyedByType(t *testing.T) {
	// Every entry must be keyed by its own Type().
	for key, provider := range DefaultRegistry() {
		require.Equal(t, key, provider.Type(), "provider registered under key %q reports Type() %q", key, provider.Type())
	}
}
