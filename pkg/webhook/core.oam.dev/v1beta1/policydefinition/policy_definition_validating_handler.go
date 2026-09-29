/*
 Copyright 2021. The KubeVela Authors.

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

package policydefinition

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
	applicationcontroller "github.com/oam-dev/kubevela/pkg/controller/core.oam.dev/v1beta1/application"
	"github.com/oam-dev/kubevela/pkg/cue/upgrade"
	"github.com/oam-dev/kubevela/pkg/definition/nsrestrict"
	"github.com/oam-dev/kubevela/pkg/oam"
	webhookutils "github.com/oam-dev/kubevela/pkg/webhook/utils"
)

var policyDefGVR = v1beta1.PolicyDefinitionGVR

// ValidatingHandler handles validation of policy definition
type ValidatingHandler struct {
	// Decoder decodes object
	Decoder admission.Decoder
	Client  client.Client
}

var _ admission.Handler = &ValidatingHandler{}

// Handle validate component definition
func (h *ValidatingHandler) Handle(ctx context.Context, req admission.Request) admission.Response {

	obj := &v1beta1.PolicyDefinition{}
	if req.Resource.String() != policyDefGVR.String() {
		err := fmt.Errorf("expect resource to be %s", policyDefGVR)
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("%s (requestUID=%s)", err.Error(), req.UID))
	}

	if req.Operation == admissionv1.Create || req.Operation == admissionv1.Update {
		var cueWarnings []string
		if err := h.Decoder.Decode(req, obj); err != nil {
			return admission.Errored(http.StatusBadRequest, fmt.Errorf("%s (requestUID=%s)", err.Error(), req.UID))
		}

		if obj.Spec.Schematic != nil && obj.Spec.Schematic.CUE != nil {

			// Validate against the effective template; with auto-upgrade is enabled
			cueTemplate := obj.Spec.Schematic.CUE.Template
			if *upgrade.EnableCUEVersionCompatibility {
				upgraded, wasUpgraded := upgrade.EnsureCueVersionCompatibility(cueTemplate, obj.Name, upgrade.PolicyKind, upgrade.TemplateAreaMain)
				if wasUpgraded {
					cueWarnings = append(cueWarnings, "CUE template uses legacy syntax that will be auto-upgraded at render time. Run `vela def compat definitions` to scan all definitions for legacy syntax.")
					cueTemplate = upgraded
				}
			}

			if err := webhookutils.ValidateCueTemplate(cueTemplate); err != nil {
				return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
			}
			if err := webhookutils.ValidateOutputResourcesExist(cueTemplate, h.Client.RESTMapper(), obj); err != nil {
				return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
			}
		}

		if obj.Spec.Version != "" {
			if err := webhookutils.ValidateSemanticVersion(obj.Spec.Version); err != nil {
				return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
			}
		}

		// Validate namespace restrictions. A malformed glob would otherwise deny
		// silently at render time, far from where it was written.
		if err := nsrestrict.ValidateObject(obj); err != nil {
			return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
		}

		revisionName := obj.GetAnnotations()[oam.AnnotationDefinitionRevisionName]
		if len(revisionName) != 0 {
			defRevName := fmt.Sprintf("%s-v%s", obj.Name, revisionName)
			if err := webhookutils.ValidateDefinitionRevision(ctx, h.Client, obj, client.ObjectKey{Namespace: obj.Namespace, Name: defRevName}); err != nil {
				return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
			}
		}

		if err := webhookutils.ValidateMultipleDefVersionsNotPresent(obj.Spec.Version, revisionName, obj.Kind); err != nil {
			return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
		}

		// Validate Application-scoped policy constraints (global=true rules, scope consistency)
		validationResult := applicationcontroller.ValidatePolicyDefinition(obj)
		validationResult.Warnings = append(validationResult.Warnings, cueWarnings...)
		if !validationResult.IsValid() {
			return admission.Denied(fmt.Sprintf("invalid PolicyDefinition: %v (requestUID=%s)", validationResult.Errors, req.UID))
		}

		if len(validationResult.Warnings) > 0 {
			return admission.ValidationResponse(true, "").WithWarnings(validationResult.Warnings...)
		}
	}
	return admission.ValidationResponse(true, "")
}

// RegisterValidatingHandler will register ComponentDefinition validation to webhook
func RegisterValidatingHandler(mgr manager.Manager) {
	server := mgr.GetWebhookServer()
	server.Register("/validating-core-oam-dev-v1beta1-policydefinitions", &webhook.Admission{Handler: &ValidatingHandler{
		Client:  mgr.GetClient(),
		Decoder: admission.NewDecoder(mgr.GetScheme()),
	}})
}
