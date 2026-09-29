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

package application

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	controller "github.com/oam-dev/kubevela/pkg/controller/core.oam.dev"
	"github.com/oam-dev/kubevela/pkg/oam/util"
	addonvalidation "github.com/oam-dev/kubevela/pkg/webhook/core.oam.dev/v1beta1/application/addon"
	webhookutils "github.com/oam-dev/kubevela/pkg/webhook/utils"
)

var _ admission.Handler = &ValidatingHandler{}

// ValidatingHandler handles application
type ValidatingHandler struct {
	Client client.Client
	// APIReader reads straight from the API server, used for the one Namespace
	// lookup a restriction's label selector needs. The cached client would start a
	// cluster-wide Namespace informer inside an admission request.
	APIReader client.Reader
	// Live is the same idea for definitions, as a full client rather than a
	// Reader: resolving a pinned revision asserts its reader back to a
	// client.Client. It sits behind the cache, not in front of it. Nil falls back
	// to Client.
	Live client.Client
	// Decoder decodes objects
	Decoder admission.Decoder

	addonValidator addonComponentValidator
}

func simplifyError(err error) error {
	switch e := err.(type) { // nolint
	case *field.Error:
		return fmt.Errorf("field \"%s\": %s error encountered, %s. ", e.Field, e.Type, e.Detail)
	default:
		return err
	}
}

func mergeErrors(errs field.ErrorList) error {
	s := ""
	for i, err := range errs {
		s += fmt.Sprintf("\n  %d) %q: %s.", i+1, err.Field, err.Detail)
	}
	return errors.New(s)
}

// Handle validate Application Spec here
func (h *ValidatingHandler) Handle(ctx context.Context, req admission.Request) admission.Response {
	// TODO(logging): this handler has no logging.

	// Decode the application
	app := &v1beta1.Application{}
	if err := h.Decoder.Decode(req, app); err != nil {
		return admission.Errored(http.StatusBadRequest, fmt.Errorf("failed to decode: %w (requestUID=%s)", err, req.UID))
	}

	if req.Namespace != "" {
		app.Namespace = req.Namespace
	}

	ctx = util.SetNamespaceInCtx(ctx, app.Namespace)

	// A quota may ask to be flagged before it refuses; the warnings ride back on an
	// admitted response.
	var warnings []string

	switch req.Operation {
	case admissionv1.Create:
		allErrs, createWarnings := h.ValidateCreate(ctx, app, req)
		warnings = createWarnings
		if len(allErrs) > 0 {
			mergedErr := mergeErrors(allErrs)
			return admission.Errored(http.StatusBadRequest, fmt.Errorf("%w (requestUID=%s)", mergedErr, req.UID))
		}

	case admissionv1.Update:
		oldApp := &v1beta1.Application{}
		if err := h.Decoder.DecodeRaw(req.AdmissionRequest.OldObject, oldApp); err != nil {
			return admission.Errored(http.StatusBadRequest, fmt.Errorf("%w (requestUID=%s)", simplifyError(err), req.UID))
		}

		if app.ObjectMeta.DeletionTimestamp.IsZero() {
			allErrs, updateWarnings := h.ValidateUpdate(ctx, app, oldApp, req)
			warnings = updateWarnings
			if len(allErrs) > 0 {
				mergedErr := mergeErrors(allErrs)
				return admission.Errored(http.StatusBadRequest, fmt.Errorf("%w (requestUID=%s)", mergedErr, req.UID))
			}
		}
	}

	return admission.ValidationResponse(true, "").WithWarnings(warnings...)
}

// RegisterValidatingHandler will register application validate handler to the webhook
func RegisterValidatingHandler(mgr manager.Manager, _ controller.Args) {
	server := mgr.GetWebhookServer()
	server.Register("/validating-core-oam-dev-v1beta1-applications", &webhook.Admission{Handler: &ValidatingHandler{
		Client:         mgr.GetClient(),
		APIReader:      mgr.GetAPIReader(),
		Live:           webhookutils.LiveClient(mgr),
		Decoder:        admission.NewDecoder(mgr.GetScheme()),
		addonValidator: addonvalidation.NewValidator(mgr.GetClient(), mgr.GetConfig()),
	}})
}
