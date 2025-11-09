/*
Copyright 2022 The KubeVela Authors.

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

package crdvalidation

import (
	"context"
	"fmt"
	"time"

	"github.com/kubevela/pkg/util/singleton"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/cmd/core/app/hooks"
)

// Hook validates that CRDs installed in the cluster are compatible with
// enabled feature gates. This prevents silent data corruption by failing
// fast at startup if CRDs are out of date.
type Hook struct {
	client.Client
}

// NewHook creates a new CRD validation hook
func NewHook() hooks.PreStartHook {
	klog.V(3).InfoS("Initializing CRD validation hook", "client", "singleton")
	return &Hook{Client: singleton.KubeClient.Get()}
}

// Name returns the hook name for logging
func (h *Hook) Name() string {
	return "CRDValidation"
}

// Run executes the CRD validation logic. It orchestrates multiple validation checks:
// 1. Compression CRD validation (ApplicationRevision, ResourceTracker)
// 2. Core CRD validation (Application, TraitDefinition, PolicyDefinition, WorkflowStepDefinition)
func (h *Hook) Run(ctx context.Context) error {
	klog.InfoS("Starting CRD validation hook")
	startTime := time.Now()

	var allErrors []error

	// 1. Validate compression-related CRDs
	klog.V(1).InfoS("Running compression CRD validation")
	if err := h.ValidateCompressionCRDs(ctx); err != nil {
		klog.ErrorS(err, "Compression CRD validation failed")
		allErrors = append(allErrors, err)
	}

	// 2. Validate core CRDs
	klog.V(1).InfoS("Running core CRD validation")
	if err := h.ValidateCoreCRDs(ctx); err != nil {
		klog.ErrorS(err, "Core CRD validation failed")
		allErrors = append(allErrors, err)
	}

	// Report overall status
	if len(allErrors) > 0 {
		klog.ErrorS(nil, "CRD validation hook failed",
			"errors", len(allErrors),
			"duration", time.Since(startTime))
		return fmt.Errorf("CRD validation failed with %d errors: %v", len(allErrors), allErrors)
	}

	klog.InfoS("CRD validation hook completed successfully",
		"duration", time.Since(startTime))
	return nil
}
