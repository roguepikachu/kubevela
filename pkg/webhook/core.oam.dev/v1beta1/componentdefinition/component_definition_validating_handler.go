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

package componentdefinition

import (
	"context"
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/pkg/appfile"
	"github.com/oam-dev/kubevela/pkg/cue/upgrade"
	"github.com/oam-dev/kubevela/pkg/definition/inherit"
	"github.com/oam-dev/kubevela/pkg/definition/nsrestrict"
	"github.com/oam-dev/kubevela/pkg/oam"
	"github.com/oam-dev/kubevela/pkg/oam/util"
	webhookutils "github.com/oam-dev/kubevela/pkg/webhook/utils"
)

var componentDefGVR = v1beta1.ComponentDefinitionGVR

// ValidatingHandler handles validation of component definition
type ValidatingHandler struct {
	// Decoder decodes object
	Decoder admission.Decoder
	Client  client.Client
	// Live reads straight from the API server, for resolving a chain against a
	// parent that may have been written moments earlier. Nil falls back to
	// Client, which is what tests supplying a fake want.
	Live client.Client
}

var _ admission.Handler = &ValidatingHandler{}

// Handle validate ComponentDefinition Spec here
func (h *ValidatingHandler) Handle(ctx context.Context, req admission.Request) admission.Response {

	obj := &v1beta1.ComponentDefinition{}
	// Advisory findings, returned with an accepted definition rather than
	// refusing it. Inheritance produces these: a parameter passed to a parent
	// that does not declare it is almost certainly a typo, but CUE accepts it.
	var warnings []string
	if req.Resource.String() != componentDefGVR.String() {
		err := fmt.Errorf("expect resource to be %s", componentDefGVR)
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("%s (requestUID=%s)", err.Error(), req.UID))
	}

	if req.Operation == admissionv1.Create || req.Operation == admissionv1.Update {
		var warnings []string
		if err := h.Decoder.Decode(req, obj); err != nil {
			return admission.Errored(http.StatusBadRequest, fmt.Errorf("%s (requestUID=%s)", err.Error(), req.UID))
		}

		// Validate workload
		if err := ValidateWorkload(h.Client.RESTMapper(), obj); err != nil {
			return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
		}

		// Judged outside the block below, which is where everything else about
		// `extends` is checked: with no CUE schematic that block is skipped and a
		// definition that composes nothing would be admitted unexamined.
		if err := webhookutils.ValidateExtendsHasTemplate(
			"ComponentDefinition", obj.Name, obj.Spec.Extends, obj.Spec.Schematic); err != nil {
			return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
		}

		// Validate CUE template
		if obj.Spec.Schematic != nil && obj.Spec.Schematic.CUE != nil {

			// Validate against the effective template; if auto-upgrade is enabled, rewrite legacy
			// syntax before validation so the template compiles correctly.
			cueTemplate := obj.Spec.Schematic.CUE.Template
			if *upgrade.EnableCUEVersionCompatibility {
				upgraded, wasUpgraded := upgrade.EnsureCueVersionCompatibility(cueTemplate, obj.Name, upgrade.ComponentKind, upgrade.TemplateAreaMain)
				if wasUpgraded {
					warnings = append(warnings, "CUE template uses legacy syntax that will be auto-upgraded at render time. Run `vela def compat definitions` to scan all definitions for legacy syntax.")
					cueTemplate = upgraded
				}
			}

			// A definition that extends another is judged against what it
			// extends. Compiling its template alone would always fail: `$super` is
			// declared nowhere in it, by design.
			if obj.Spec.Extends != "" {
				var ancestors []inherit.Level
				err := webhookutils.ReadWithLiveRetry(h.Client, h.Live, func(cli client.Client) error {
					var e error
					ancestors, e = appfile.ComponentAncestors(ctx, cli, obj)
					return e
				})
				if err != nil {
					return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
				}
				warns, err := webhookutils.ValidateInheritedTemplate(
					ctx, obj.Name, cueTemplate, ancestors, inherit.ComponentSurface,
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

		// Validate semantic version
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

		// Validate revision
		revisionName := obj.GetAnnotations()[oam.AnnotationDefinitionRevisionName]
		if len(revisionName) != 0 {
			defRevName := fmt.Sprintf("%s-v%s", obj.Name, revisionName)
			if err := webhookutils.ValidateDefinitionRevision(ctx, h.Client, obj, client.ObjectKey{Namespace: obj.Namespace, Name: defRevName}); err != nil {
				return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
			}
		}

		// Check version conflicts
		if err := webhookutils.ValidateMultipleDefVersionsNotPresent(obj.Spec.Version, revisionName, obj.Kind); err != nil {
			return admission.Denied(fmt.Sprintf("%s (requestUID=%s)", err.Error(), req.UID))
		}

		if len(warnings) > 0 {
			return admission.ValidationResponse(true, "").WithWarnings(warnings...)
		}
	}
	resp := admission.ValidationResponse(true, "")
	resp.Warnings = warnings
	return resp
}

// RegisterValidatingHandler will register ComponentDefinition validation to webhook
func RegisterValidatingHandler(mgr manager.Manager) {
	server := mgr.GetWebhookServer()
	server.Register("/validating-core-oam-dev-v1beta1-componentdefinitions", &webhook.Admission{Handler: &ValidatingHandler{
		Client:  mgr.GetClient(),
		Live:    webhookutils.LiveClient(mgr),
		Decoder: admission.NewDecoder(mgr.GetScheme()),
	}})
}

// ValidateWorkload validates whether the Workload field is valid
func ValidateWorkload(mapper meta.RESTMapper, cd *v1beta1.ComponentDefinition) error {

	// If the Type and Definition are all empty, it will be rejected, unless this
	// definition extends another: it then renders whatever its parent renders,
	// and saying nothing about the workload means the parent's answer.
	//
	// That only holds for a definition with a template to call the parent from.
	// Without one nothing reaches the parent, so the workload would stay empty
	// and the definition would be admitted describing nothing.
	if cd.Spec.Workload.Type == "" && cd.Spec.Workload.Definition == (common.WorkloadGVK{}) {
		if cd.Spec.Extends != "" {
			if cd.Spec.Schematic == nil || cd.Spec.Schematic.CUE == nil {
				return fmt.Errorf(
					"ComponentDefinition %s extends %s but has no CUE template to call it from; "+
						"add a `%s: properties: {...}` block, or state the workload itself",
					cd.Name, cd.Spec.Extends, inherit.SuperField)
			}
			return nil
		}
		return fmt.Errorf("neither the type nor the definition of the workload field in the ComponentDefinition %s can be empty", cd.Name)
	}

	// if Type and Definitiondon‘t point to the same workloaddefinition, it will be rejected.
	if cd.Spec.Workload.Type != "" && cd.Spec.Workload.Definition != (common.WorkloadGVK{}) {
		defRef, err := util.ConvertWorkloadGVK2Definition(mapper, cd.Spec.Workload.Definition)
		if err != nil {
			return err
		}
		if defRef.Name != cd.Spec.Workload.Type {
			return fmt.Errorf("the type and the definition of the workload field in ComponentDefinition %s should represent the same workload", cd.Name)
		}
	}
	return nil
}
