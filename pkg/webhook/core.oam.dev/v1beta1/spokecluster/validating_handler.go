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
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

var _ admission.Handler = &ValidatingHandler{}

// ValidatingHandler validates SpokeCluster resources on Create and Update.
// It carries only a Decoder: every rule in Validate is stateless, so no
// client.Client is needed here (see design.md "Handler shape").
type ValidatingHandler struct {
	Decoder admission.Decoder
}

// Handle validates the SpokeCluster carried by the admission request.
func (h *ValidatingHandler) Handle(_ context.Context, req admission.Request) admission.Response {
	if req.Resource.String() != v1beta1.SpokeClusterGVR.String() {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("expect resource to be %s", v1beta1.SpokeClusterGVR))
	}

	if req.Operation == admissionv1.Create || req.Operation == admissionv1.Update {
		sc := &v1beta1.SpokeCluster{}
		if err := h.Decoder.Decode(req, sc); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
		if errs := Validate(sc); len(errs) > 0 {
			return admission.Denied(errs.ToAggregate().Error())
		}
	}

	return admission.ValidationResponse(true, "")
}

// RegisterValidatingHandler registers the SpokeCluster validating webhook on
// the given manager's webhook server.
func RegisterValidatingHandler(mgr manager.Manager) {
	mgr.GetWebhookServer().Register("/validating-core-oam-dev-v1beta1-spokeclusters", &webhook.Admission{
		Handler: &ValidatingHandler{Decoder: admission.NewDecoder(mgr.GetScheme())},
	})
}
