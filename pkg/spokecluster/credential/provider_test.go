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

	. "github.com/onsi/ginkgo/v2"
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

func (f fakeProvider) Materialize(_ context.Context, _ client.Reader, _ *v1beta1.SpokeCluster) (*Materialized, error) {
	return &Materialized{}, nil
}

var _ = It("HasClientCert", func() {
	t := GinkgoT()
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
		By(name, func() {
			require.Equal(t, tc.want, tc.m.HasClientCert())
		})
	}
})

var _ = It("RegistryFor", func() {
	t := GinkgoT()
	provider := fakeProvider{credType: v1beta1.CredentialTypeKubeconfig}
	reg := Registry{v1beta1.CredentialTypeKubeconfig: provider}

	By("registered type returns its provider", func() {
		got, err := reg.For(v1beta1.CredentialTypeKubeconfig)
		require.NoError(t, err)
		require.Equal(t, v1beta1.CredentialTypeKubeconfig, got.Type())
	})

	By("unregistered type returns a descriptive error and does not panic", func() {
		require.NotPanics(t, func() {
			got, err := reg.For(v1beta1.CredentialType("unknown"))
			require.Nil(t, got)
			require.Error(t, err)
			require.Contains(t, err.Error(), "unknown")
		})
	})
})

var _ = It("DefaultRegistryKeyedByType", func() {
	t := GinkgoT()
	// Every entry must be keyed by its own Type().
	for key, provider := range DefaultRegistry() {
		require.Equal(t, key, provider.Type(), "provider registered under key %q reports Type() %q", key, provider.Type())
	}
})

var _ = It("DefaultRegistryHasAllArms", func() {
	t := GinkgoT()
	// All four credential arms are registered so lookups never fall through to a
	// generic "no provider" error.
	reg := DefaultRegistry()
	for _, ct := range []v1beta1.CredentialType{
		v1beta1.CredentialTypeKubeconfig,
		v1beta1.CredentialTypeAWS,
		v1beta1.CredentialTypeAzure,
		v1beta1.CredentialTypeGCP,
	} {
		_, err := reg.For(ct)
		require.NoError(t, err, "credential type %q should be registered", ct)
	}
})

var _ = It("StubProvidersNotImplemented", func() {
	t := GinkgoT()
	// The azure and gcp arms are registered stubs: they materialize to an
	// arm-specific not-implemented error, not a partial result. When they are
	// implemented, these assertions flip and signal the tests to update.
	sc := &v1beta1.SpokeCluster{}
	for name, p := range map[string]Provider{
		"azure": NewAzureProvider(),
		"gcp":   NewGCPProvider(),
	} {
		By(name, func() {
			m, err := p.Materialize(context.Background(), nil, sc)
			require.Nil(t, m)
			require.Error(t, err)
			require.Contains(t, err.Error(), "not implemented")
		})
	}
})
