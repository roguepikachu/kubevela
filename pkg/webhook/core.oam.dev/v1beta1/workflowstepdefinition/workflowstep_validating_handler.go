/*
Copyright 2024 The KubeVela Authors.

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

// Package workflowstepdefinition provides admission control validation
// for WorkflowStepDefinition resources in KubeVela.
package workflowstepdefinition

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
	"github.com/oam-dev/kubevela/pkg/cue/upgrade"
	"github.com/oam-dev/kubevela/pkg/definition/nsrestrict"
	"github.com/oam-dev/kubevela/pkg/oam"
	webhookutils "github.com/oam-dev/kubevela/pkg/webhook/utils"
)

const (
	// ValidationWebhookPath defines the HTTP path for the validation webhook
	ValidationWebhookPath = "/validating-core-oam-dev-v1beta1-workflowstepdefinitions"
)

var (
	workflowStepDefGVR = v1beta1.WorkflowStepDefinitionGVR
)

// ValidatingHandler handles validation of WorkflowStepDefinition resources.
type ValidatingHandler struct {
	Decoder admission.Decoder
	Client  client.Client
}

// InjectClient injects the Kubernetes client into the handler.
func (h *ValidatingHandler) InjectClient(c client.Client) error {
	h.Client = c
	return nil
}

// InjectDecoder injects the admission decoder into the handler.
func (h *ValidatingHandler) InjectDecoder(d admission.Decoder) error {
	h.Decoder = d
	return nil
}

// Handle validates WorkflowStepDefinition resources during admission control.
func (h *ValidatingHandler) Handle(ctx context.Context, req admission.Request) admission.Response {

	// Validate resource type
	if req.Resource.String() != workflowStepDefGVR.String() {
		err := fmt.Errorf("expected resource to be %s, got %s", workflowStepDefGVR, req.Resource.String())
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("%s (requestUID=%s)", err.Error(), req.UID))
	}

	// Only validate create and update operations
	if req.Operation != admissionv1.Create && req.Operation != admissionv1.Update {
		return admission.ValidationResponse(true, "Operation does not require validation")
	}

	// Decode the object
	obj := &v1beta1.WorkflowStepDefinition{}
	if err := h.Decoder.Decode(req, obj); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("failed to decode: %s (requestUID=%s)", err.Error(), req.UID))
	}

	// Validate CUE template
	var warnings []string
	if obj.Spec.Schematic != nil && obj.Spec.Schematic.CUE != nil {

		cueTemplate := obj.Spec.Schematic.CUE.Template
		if *upgrade.EnableCUEVersionCompatibility {
			upgraded, wasUpgraded := upgrade.EnsureCueVersionCompatibility(cueTemplate, obj.Name, upgrade.WorkflowStepKind, upgrade.TemplateAreaMain)
			if wasUpgraded {
				warnings = append(warnings, "CUE template uses legacy syntax that will be auto-upgraded at render time. Run `vela def compat definitions` to scan all definitions for legacy syntax.")
				cueTemplate = upgraded
			}
		}

		if err := webhookutils.ValidateOutputResourcesExist(cueTemplate, h.Client.RESTMapper(), obj); err != nil {
			return admission.Denied(fmt.Sprintf("output resource validation failed: %s (requestUID=%s)", err.Error(), req.UID))
		}
	}

	// Validate semantic version
	if obj.Spec.Version != "" {
		if err := webhookutils.ValidateSemanticVersion(obj.Spec.Version); err != nil {
			return admission.Denied(fmt.Sprintf("semantic version validation failed: %s (requestUID=%s)", err.Error(), req.UID))
		}
	}

	// Validate namespace restrictions. A malformed glob would otherwise deny
	// silently at render time, far from where it was written.
	if err := nsrestrict.ValidateObject(obj); err != nil {
		return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
	}

	// Validate version conflicts
	revisionName := obj.Annotations[oam.AnnotationDefinitionRevisionName]
	if err := webhookutils.ValidateMultipleDefVersionsNotPresent(obj.Spec.Version, revisionName, obj.Kind); err != nil {
		return admission.Denied(fmt.Sprintf("definition version conflict: %s (requestUID=%s)", err.Error(), req.UID))
	}

	if len(warnings) > 0 {
		return admission.ValidationResponse(true, "").WithWarnings(warnings...)
	}
	return admission.ValidationResponse(true, "Validation passed")
}

// RegisterValidatingHandler registers the WorkflowStepDefinition validation webhook with the manager.
func RegisterValidatingHandler(mgr manager.Manager) {

	server := mgr.GetWebhookServer()
	server.Register(ValidationWebhookPath, &webhook.Admission{
		Handler: &ValidatingHandler{
			Client:  mgr.GetClient(),
			Decoder: admission.NewDecoder(mgr.GetScheme()),
		},
	})
}
