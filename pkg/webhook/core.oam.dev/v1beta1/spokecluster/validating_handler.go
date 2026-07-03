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
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
)

// ValidatingHandler validates SpokeCluster create and update requests.
type ValidatingHandler struct {
	Client  client.Client
	Decoder admission.Decoder
}

var _ admission.Handler = &ValidatingHandler{}

var spokeClusterGVR = v1beta1.SpokeClusterGVR

// Handle validates a SpokeCluster on create and update.
func (h *ValidatingHandler) Handle(_ context.Context, req admission.Request) admission.Response {
	if req.Resource.String() != spokeClusterGVR.String() {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("expect resource to be %s", spokeClusterGVR))
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

// RegisterValidatingHandler registers the SpokeCluster validating webhook.
func RegisterValidatingHandler(mgr manager.Manager) {
	server := mgr.GetWebhookServer()
	server.Register("/validating-core-oam-dev-v1beta1-spokeclusters", &webhook.Admission{Handler: &ValidatingHandler{
		Client:  mgr.GetClient(),
		Decoder: admission.NewDecoder(mgr.GetScheme()),
	}})
}
