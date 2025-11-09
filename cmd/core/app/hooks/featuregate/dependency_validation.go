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

package featuregate

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apiserver/pkg/util/feature"
	"k8s.io/component-base/featuregate"
	"k8s.io/klog/v2"

	"github.com/oam-dev/kubevela/cmd/core/app/hooks"
	"github.com/oam-dev/kubevela/pkg/features"
)

// DependencyValidator validates feature gate dependencies and conflicts
type DependencyValidator struct{}

// NewHook creates a new feature gate dependency validation hook
func NewHook() hooks.PreStartHook {
	klog.V(3).InfoS("Initializing feature gate dependency validation hook")
	return &DependencyValidator{}
}

// Name returns the hook name for logging
func (v *DependencyValidator) Name() string {
	return "FeatureGateDependencyValidation"
}

// FeatureDependency defines a dependency relationship between feature gates
type FeatureDependency struct {
	Feature      featuregate.Feature
	Requires     []featuregate.Feature
	ConflictWith []featuregate.Feature
	WarningOnly  bool
	Message      string
}

// Run executes the feature gate dependency validation
func (v *DependencyValidator) Run(ctx context.Context) error {
	klog.InfoS("Starting feature gate dependency validation")
	startTime := time.Now()

	dependencies := []FeatureDependency{
		{
			Feature:  features.SharedDefinitionStorageForApplicationRevision,
			Requires: []featuregate.Feature{features.InformerCacheFilterUnnecessaryFields},
			Message:  "SharedDefinitionStorageForApplicationRevision requires InformerCacheFilterUnnecessaryFields to be enabled for proper cache filtering",
		},
		{
			Feature:      features.ApplyOnce,
			ConflictWith: []featuregate.Feature{features.GzipResourceTracker, features.ZstdResourceTracker},
			WarningOnly:  true,
			Message:      "ApplyOnce disables ResourceTracker data storage, which conflicts with ResourceTracker compression features",
		},
	}

	var validationErrors []error
	var warnings []string

	// Check each dependency
	for _, dep := range dependencies {
		if !feature.DefaultMutableFeatureGate.Enabled(dep.Feature) {
			klog.V(3).InfoS("Feature gate not enabled, skipping dependency check",
				"feature", dep.Feature)
			continue
		}

		klog.V(2).InfoS("Checking dependencies for feature gate",
			"feature", dep.Feature,
			"requires", dep.Requires,
			"conflicts", dep.ConflictWith)

		// Check required dependencies
		for _, required := range dep.Requires {
			if !feature.DefaultMutableFeatureGate.Enabled(required) {
				errMsg := fmt.Sprintf("Feature %s is enabled but requires %s to be enabled: %s",
					dep.Feature, required, dep.Message)

				if dep.WarningOnly {
					klog.V(1).InfoS(errMsg)
					warnings = append(warnings, errMsg)
				} else {
					klog.ErrorS(nil, errMsg)
					validationErrors = append(validationErrors, fmt.Errorf("%s", errMsg))
				}
			} else {
				klog.V(2).InfoS("Required dependency satisfied",
					"feature", dep.Feature,
					"required", required)
			}
		}

		// Check for conflicts
		for _, conflict := range dep.ConflictWith {
			if feature.DefaultMutableFeatureGate.Enabled(conflict) {
				errMsg := fmt.Sprintf("Feature %s conflicts with %s: %s",
					dep.Feature, conflict, dep.Message)

				if dep.WarningOnly {
					klog.V(1).InfoS(errMsg)
					warnings = append(warnings, errMsg)
				} else {
					klog.ErrorS(nil, errMsg)
					validationErrors = append(validationErrors, fmt.Errorf("%s", errMsg))
				}
			} else {
				klog.V(3).InfoS("No conflict detected",
					"feature", dep.Feature,
					"potentialConflict", conflict)
			}
		}
	}

	// Check compression priority (informational)
	v.checkCompressionPriority()

	// Check for experimental features in production
	v.checkExperimentalFeatures()

	if len(warnings) > 0 {
		klog.InfoS("Feature gate validation completed with warnings",
			"warnings", len(warnings),
			"duration", time.Since(startTime))
	}

	if len(validationErrors) > 0 {
		klog.ErrorS(nil, "Feature gate dependency validation failed",
			"errors", len(validationErrors),
			"duration", time.Since(startTime))
		return fmt.Errorf("feature gate dependency validation failed with %d errors: %v",
			len(validationErrors), validationErrors)
	}

	klog.InfoS("Feature gate dependency validation completed successfully",
		"duration", time.Since(startTime))
	return nil
}

// checkCompressionPriority logs information about compression feature priority
func (v *DependencyValidator) checkCompressionPriority() {
	// Check ApplicationRevision compression
	appRevZstd := feature.DefaultMutableFeatureGate.Enabled(features.ZstdApplicationRevision)
	appRevGzip := feature.DefaultMutableFeatureGate.Enabled(features.GzipApplicationRevision)

	if appRevZstd && appRevGzip {
		klog.InfoS("Multiple ApplicationRevision compression types enabled",
			"priority", "Zstd will take priority over Gzip",
			"zstd", appRevZstd,
			"gzip", appRevGzip)
	}

	// Check ResourceTracker compression
	rtZstd := feature.DefaultMutableFeatureGate.Enabled(features.ZstdResourceTracker)
	rtGzip := feature.DefaultMutableFeatureGate.Enabled(features.GzipResourceTracker)

	if rtZstd && rtGzip {
		klog.InfoS("Multiple ResourceTracker compression types enabled",
			"priority", "Zstd will take priority over Gzip",
			"zstd", rtZstd,
			"gzip", rtGzip)
	}
}

// checkExperimentalFeatures warns about experimental features in use
func (v *DependencyValidator) checkExperimentalFeatures() {
	experimentalFeatures := []featuregate.Feature{
		features.ZstdApplicationRevision,
		features.GzipApplicationRevision,
		features.ZstdResourceTracker,
		features.GzipResourceTracker,
		features.ApplyOnce,
	}

	enabledExperimental := []featuregate.Feature{}
	for _, f := range experimentalFeatures {
		if feature.DefaultMutableFeatureGate.Enabled(f) {
			enabledExperimental = append(enabledExperimental, f)
		}
	}

	if len(enabledExperimental) > 0 {
		klog.V(1).InfoS("Experimental/Alpha features are enabled",
			"features", enabledExperimental,
			"warning", "These features are not recommended for production use")
	}
}
