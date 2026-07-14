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

package spokecluster

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// newTestDecoder builds an admission.Decoder against a scheme with both the
// core k8s types and the vela v1beta1 types registered, so it can decode
// SpokeCluster admission requests exactly as the manager's decoder would.
func newTestDecoder(t *testing.T) admission.Decoder {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, k8sscheme.AddToScheme(scheme))
	require.NoError(t, v1beta1.AddToScheme(scheme))
	return admission.NewDecoder(scheme)
}

// spokeClusterGVR mirrors v1beta1.SpokeClusterGVR as the metav1 type used on
// admission.Request.Resource.
var spokeClusterGVR = metav1.GroupVersionResource{
	Group:    v1beta1.SpokeClusterGVR.Group,
	Version:  v1beta1.SpokeClusterGVR.Version,
	Resource: v1beta1.SpokeClusterGVR.Resource,
}

// newSpokeClusterRequest marshals sc into an admission.Request with the given
// operation and the spokeclusters GVR, as the apiserver would send it.
func newSpokeClusterRequest(t *testing.T, sc *v1beta1.SpokeCluster, op admissionv1.Operation) admission.Request {
	t.Helper()
	raw, err := json.Marshal(sc)
	require.NoError(t, err)
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Resource:  spokeClusterGVR,
		Operation: op,
		Object:    runtime.RawExtension{Raw: raw},
	}}
}

func TestValidatingHandler_Handle(t *testing.T) {
	handler := &ValidatingHandler{Decoder: newTestDecoder(t)}

	t.Run("valid kubeconfig spoke on create is allowed", func(t *testing.T) {
		req := newSpokeClusterRequest(t, validKubeconfigSpoke(), admissionv1.Create)
		resp := handler.Handle(context.Background(), req)
		require.True(t, resp.Allowed, "response: %+v", resp.Result)
	})

	t.Run("provision mode on create is denied", func(t *testing.T) {
		sc := validKubeconfigSpoke()
		sc.Spec.Mode = v1beta1.SpokeClusterModeProvision
		req := newSpokeClusterRequest(t, sc, admissionv1.Create)
		resp := handler.Handle(context.Background(), req)
		require.False(t, resp.Allowed)
	})

	t.Run("wrong resource GVR is a bad request", func(t *testing.T) {
		req := newSpokeClusterRequest(t, validKubeconfigSpoke(), admissionv1.Create)
		req.Resource = metav1.GroupVersionResource{Group: "core.oam.dev", Version: "v1beta1", Resource: "applications"}
		resp := handler.Handle(context.Background(), req)
		require.False(t, resp.Allowed)
	})

	t.Run("delete is admitted without decoding", func(t *testing.T) {
		req := newSpokeClusterRequest(t, validKubeconfigSpoke(), admissionv1.Delete)
		resp := handler.Handle(context.Background(), req)
		require.True(t, resp.Allowed, "response: %+v", resp.Result)
	})
}

// patchValue looks up a jsonpatch operation by path (add or replace, either
// is a valid outcome depending on whether the field was present in the raw
// request) and returns its value.
func patchValue(t *testing.T, patches []jsonpatch.Operation, path string) interface{} {
	t.Helper()
	for _, p := range patches {
		if p.Path == path {
			return p.Value
		}
	}
	t.Fatalf("no patch found for path %s (patches: %+v)", path, patches)
	return nil
}

func TestMutatingHandler_Handle(t *testing.T) {
	handler := &MutatingHandler{Decoder: newTestDecoder(t)}

	sc := validKubeconfigSpoke()
	sc.Spec.Mode = ""
	sc.Spec.ProbeIntervalSeconds = 0
	sc.Spec.ProbeTimeoutSeconds = 0
	sc.Spec.DeletionPolicy = ""
	sc.Spec.Credential.Kubeconfig.SecretRef.Key = ""

	req := newSpokeClusterRequest(t, sc, admissionv1.Create)
	resp := handler.Handle(context.Background(), req)

	require.True(t, resp.Allowed, "response: %+v", resp.Result)
	require.NotEmpty(t, resp.Patches, "expected defaulting to produce at least one JSON patch")

	require.Equal(t, string(v1beta1.SpokeClusterModeConnect), patchValue(t, resp.Patches, "/spec/mode"))
	require.EqualValues(t, 30, patchValue(t, resp.Patches, "/spec/probeIntervalSeconds"))
	require.EqualValues(t, 10, patchValue(t, resp.Patches, "/spec/probeTimeoutSeconds"))
	require.Equal(t, string(v1beta1.SpokeDeletionPolicyDetach), patchValue(t, resp.Patches, "/spec/deletionPolicy"))
	require.Equal(t, defaultSecretKey, patchValue(t, resp.Patches, "/spec/credential/kubeconfig/secretRef/key"))
}
