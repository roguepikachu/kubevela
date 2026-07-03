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

package spokecluster

import (
	"context"
	"encoding/json"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// MutatingHandler defaults SpokeCluster fields on create and update.
type MutatingHandler struct {
	Decoder admission.Decoder
}

var _ admission.Handler = &MutatingHandler{}

// Handle applies defaults and returns a JSON patch.
func (h *MutatingHandler) Handle(_ context.Context, req admission.Request) admission.Response {
	sc := &v1beta1.SpokeCluster{}
	if err := h.Decoder.Decode(req, sc); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	Default(sc)
	marshalled, err := json.Marshal(sc)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.AdmissionRequest.Object.Raw, marshalled)
}

// RegisterMutatingHandler registers the SpokeCluster mutating webhook.
func RegisterMutatingHandler(mgr manager.Manager) {
	server := mgr.GetWebhookServer()
	server.Register("/mutating-core-oam-dev-v1beta1-spokeclusters", &webhook.Admission{Handler: &MutatingHandler{
		Decoder: admission.NewDecoder(mgr.GetScheme()),
	}})
}
