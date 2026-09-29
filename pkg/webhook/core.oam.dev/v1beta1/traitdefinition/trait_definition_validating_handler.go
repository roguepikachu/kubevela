/*
Copyright 2021 The KubeVela Authors.

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

package traitdefinition

import (
	"context"
	"fmt"
	"net/http"

	"github.com/pkg/errors"
	admissionv1 "k8s.io/api/admission/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/appfile"
	controller "github.com/oam-dev/kubevela/pkg/controller/core.oam.dev"
	"github.com/oam-dev/kubevela/pkg/cue/upgrade"
	"github.com/oam-dev/kubevela/pkg/definition/inherit"
	"github.com/oam-dev/kubevela/pkg/definition/nsrestrict"
	"github.com/oam-dev/kubevela/pkg/oam"
	webhookutils "github.com/oam-dev/kubevela/pkg/webhook/utils"
)

const (
	errValidateDefRef = "error occurs when validating definition reference"

	failInfoDefRefOmitted = "if definition reference is omitted, patch or output with GVK is required"
)

var traitDefGVR = v1beta1.TraitDefinitionGVR

// ValidatingHandler handles validation of trait definition
type ValidatingHandler struct {
	Client client.Client
	// Live reads straight from the API server; see the component handler.
	Live client.Client

	// Decoder decodes object
	Decoder admission.Decoder
	// Validators validate objects
	Validators []TraitDefValidator
}

// TraitDefValidator validate trait definition
type TraitDefValidator interface {
	Validate(context.Context, v1beta1.TraitDefinition) error
}

// TraitDefValidatorFn implements TraitDefValidator
type TraitDefValidatorFn func(context.Context, v1beta1.TraitDefinition) error

// Validate implements TraitDefValidator method
func (fn TraitDefValidatorFn) Validate(ctx context.Context, td v1beta1.TraitDefinition) error {
	return fn(ctx, td)
}

var _ admission.Handler = &ValidatingHandler{}

// Handle validate trait definition
func (h *ValidatingHandler) Handle(ctx context.Context, req admission.Request) admission.Response {

	obj := &v1beta1.TraitDefinition{}
	// Advisory findings, returned with an accepted definition rather than
	// refusing it.
	var warnings []string
	if req.Resource.String() != traitDefGVR.String() {
		err := fmt.Errorf("expect resource to be %s", traitDefGVR)
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("%s (requestUID=%s)", err.Error(), req.UID))
	}

	if req.Operation == admissionv1.Create || req.Operation == admissionv1.Update {
		if err := h.Decoder.Decode(req, obj); err != nil {
			return admission.Errored(http.StatusBadRequest, fmt.Errorf("%s (requestUID=%s)", err.Error(), req.UID))
		}

		for _, validator := range h.Validators {
			if err := validator.Validate(ctx, *obj); err != nil {
				return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
			}
		}

		// Judged outside the block below, which is where everything else about
		// `extends` is checked: with no CUE schematic that block is skipped and a
		// definition that composes nothing would be admitted unexamined.
		if err := webhookutils.ValidateExtendsHasTemplate(
			"TraitDefinition", obj.Name, obj.Spec.Extends, obj.Spec.Schematic); err != nil {
			return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
		}

		// validate cueTemplate
		if obj.Spec.Schematic != nil && obj.Spec.Schematic.CUE != nil {

			// Validate against the effective template; with auto-upgrade is enabled
			cueTemplate := obj.Spec.Schematic.CUE.Template
			if *upgrade.EnableCUEVersionCompatibility {
				upgraded, wasUpgraded := upgrade.EnsureCueVersionCompatibility(cueTemplate, obj.Name, upgrade.TraitKind, upgrade.TemplateAreaMain)
				if wasUpgraded {
					warnings = append(warnings, "CUE template uses legacy syntax that will be auto-upgraded at render time. Run `vela def compat definitions` to scan all definitions for legacy syntax.")
					cueTemplate = upgraded
				}
			}

			// A trait that extends another is judged against what it extends.
			// Compiling its template alone would always fail: `$super` is declared
			// nowhere in it, by design.
			if obj.Spec.Extends != "" {
				var ancestors []inherit.Level
				err := webhookutils.ReadWithLiveRetry(h.Client, h.Live, func(cli client.Client) error {
					var e error
					ancestors, e = appfile.TraitAncestors(ctx, cli, obj)
					return e
				})
				if err != nil {
					return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
				}
				warns, err := webhookutils.ValidateInheritedTemplate(
					ctx, obj.Name, cueTemplate, ancestors, inherit.TraitSurface,
					webhookutils.StatusSources(obj.Spec.Status)...)
				if err != nil {
					return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
				}
				warnings = append(warnings, warns...)
			} else if err := webhookutils.ValidateCuexTemplate(ctx, cueTemplate); err != nil {
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

		version := obj.Spec.Version
		if err := webhookutils.ValidateMultipleDefVersionsNotPresent(version, revisionName, obj.Kind); err != nil {
			return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
		}
	}
	resp := admission.ValidationResponse(true, "")
	resp.Warnings = warnings
	return resp
}

// RegisterValidatingHandler will register TraitDefinition validation to webhook
func RegisterValidatingHandler(mgr manager.Manager, _ controller.Args) {
	server := mgr.GetWebhookServer()
	server.Register("/validating-core-oam-dev-v1beta1-traitdefinitions", &webhook.Admission{Handler: &ValidatingHandler{
		Client:  mgr.GetClient(),
		Live:    webhookutils.LiveClient(mgr),
		Decoder: admission.NewDecoder(mgr.GetScheme()),
		Validators: []TraitDefValidator{
			TraitDefValidatorFn(ValidateDefinitionReference),
			// add more validators here
		},
	}})
}

// ValidateDefinitionReference validates whether the trait definition is valid if
// its `.spec.reference` field is unset.
// It's valid if
// it has at least one output, and all outputs must have GVK
// or it has no output but has a patch
// or it has a patch and outputs, and all outputs must have GVK
// TODO(roywang) currently we only validate whether it contains CUE template.
// Further validation, e.g., output with GVK, valid patch, etc, remains to be done.
func ValidateDefinitionReference(_ context.Context, td v1beta1.TraitDefinition) error {
	if len(td.Spec.Reference.Name) > 0 {
		return nil
	}
	capability, err := appfile.ConvertTemplateJSON2Object(td.Name, td.Spec.Extension, td.Spec.Schematic)
	if err != nil {
		return errors.WithMessage(err, errValidateDefRef)
	}
	if capability.CueTemplate == "" {
		return errors.New(failInfoDefRefOmitted)

	}
	return nil
}
