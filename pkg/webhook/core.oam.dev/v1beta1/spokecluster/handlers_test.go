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
	"fmt"

	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"gomodules.xyz/jsonpatch/v2"
	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// spokeClusterGVR mirrors v1beta1.SpokeClusterGVR as the metav1 type used on
// admission.Request.Resource.
var spokeClusterGVR = metav1.GroupVersionResource{
	Group:    v1beta1.SpokeClusterGVR.Group,
	Version:  v1beta1.SpokeClusterGVR.Version,
	Resource: v1beta1.SpokeClusterGVR.Resource,
}

// newSpokeClusterRequest marshals sc into an admission.Request with the given
// operation and the spokeclusters GVR, as the apiserver would send it.
func newSpokeClusterRequest(sc *v1beta1.SpokeCluster, op admissionv1.Operation) admission.Request {
	raw, err := json.Marshal(sc)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Resource:  spokeClusterGVR,
		Operation: op,
		Object:    runtime.RawExtension{Raw: raw},
	}}
}

// patchValue looks up a jsonpatch operation by path (add or replace, either
// is a valid outcome depending on whether the field was present in the raw
// request) and returns its value.
func patchValue(patches []jsonpatch.Operation, path string) interface{} {
	for _, p := range patches {
		if p.Path == path {
			return p.Value
		}
	}
	Fail(fmt.Sprintf("no patch found for path %s (patches: %+v)", path, patches))
	return nil
}

var _ = Describe("ValidatingHandler", func() {
	var handler *ValidatingHandler

	BeforeEach(func() {
		handler = &ValidatingHandler{Decoder: decoder}
	})

	It("allows a valid kubeconfig spoke on create", func() {
		req := newSpokeClusterRequest(validKubeconfigSpoke(), admissionv1.Create)
		resp := handler.Handle(context.Background(), req)
		gomega.Expect(resp.Allowed).To(gomega.BeTrue(), "response: %+v", resp.Result)
	})

	It("denies provision mode on create", func() {
		sc := validKubeconfigSpoke()
		sc.Spec.Mode = v1beta1.SpokeClusterModeProvision
		req := newSpokeClusterRequest(sc, admissionv1.Create)
		resp := handler.Handle(context.Background(), req)
		gomega.Expect(resp.Allowed).To(gomega.BeFalse())
	})

	It("bad-requests a mismatched resource GVR", func() {
		req := newSpokeClusterRequest(validKubeconfigSpoke(), admissionv1.Create)
		req.Resource = metav1.GroupVersionResource{Group: "core.oam.dev", Version: "v1beta1", Resource: "applications"}
		resp := handler.Handle(context.Background(), req)
		gomega.Expect(resp.Allowed).To(gomega.BeFalse())
	})

	It("admits delete without decoding", func() {
		req := newSpokeClusterRequest(validKubeconfigSpoke(), admissionv1.Delete)
		resp := handler.Handle(context.Background(), req)
		gomega.Expect(resp.Allowed).To(gomega.BeTrue(), "response: %+v", resp.Result)
	})
})

var _ = Describe("MutatingHandler", func() {
	It("applies defaults and returns them as JSON patches", func() {
		handler := &MutatingHandler{Decoder: decoder}

		sc := validKubeconfigSpoke()
		sc.Spec.Mode = ""
		sc.Spec.ProbeIntervalSeconds = 0
		sc.Spec.ProbeTimeoutSeconds = 0
		sc.Spec.DeletionPolicy = ""
		sc.Spec.Credential.Kubeconfig.SecretRef.Key = ""

		req := newSpokeClusterRequest(sc, admissionv1.Create)
		resp := handler.Handle(context.Background(), req)

		gomega.Expect(resp.Allowed).To(gomega.BeTrue(), "response: %+v", resp.Result)
		gomega.Expect(resp.Patches).NotTo(gomega.BeEmpty(), "expected defaulting to produce at least one JSON patch")

		gomega.Expect(patchValue(resp.Patches, "/spec/mode")).To(gomega.Equal(string(v1beta1.SpokeClusterModeConnect)))
		gomega.Expect(patchValue(resp.Patches, "/spec/probeIntervalSeconds")).To(gomega.BeEquivalentTo(30))
		gomega.Expect(patchValue(resp.Patches, "/spec/probeTimeoutSeconds")).To(gomega.BeEquivalentTo(10))
		gomega.Expect(patchValue(resp.Patches, "/spec/deletionPolicy")).To(gomega.Equal(string(v1beta1.SpokeDeletionPolicyDetach)))
		gomega.Expect(patchValue(resp.Patches, "/spec/credential/kubeconfig/secretRef/key")).To(gomega.Equal(defaultSecretKey))
	})
})
