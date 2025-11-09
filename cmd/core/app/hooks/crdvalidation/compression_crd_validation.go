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

	"github.com/kubevela/pkg/util/compression"
	"github.com/kubevela/pkg/util/k8s"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/util/feature"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/pkg/features"
	"github.com/oam-dev/kubevela/pkg/oam"
)

// ValidateCompressionCRDs validates that compression-related CRDs are compatible
// with enabled feature gates. This includes ApplicationRevision and ResourceTracker.
//
// Compression validation is critical because:
// 1. Compression features (Zstd, Gzip) require CRD schema support for compression fields
// 2. Without proper CRD support, compressed data will be lost or corrupted
// 3. The controller expects specific compression field structure when features are enabled
// 4. Different compression types have different priorities (Zstd > Gzip)
//
// This function:
// - Checks which compression feature gates are enabled
// - Validates ApplicationRevision CRD if its compression features are enabled
// - Validates ResourceTracker CRD if its compression features are enabled
// - Logs priority information when multiple compression types are enabled
// - Skips validation entirely if no compression features are enabled
func (h *Hook) ValidateCompressionCRDs(ctx context.Context) error {
	klog.InfoS("Starting compression CRD validation")

	// Check ApplicationRevision compression features
	appRevZstd := feature.DefaultMutableFeatureGate.Enabled(features.ZstdApplicationRevision)
	appRevGzip := feature.DefaultMutableFeatureGate.Enabled(features.GzipApplicationRevision)

	// Check ResourceTracker compression features
	rtZstd := feature.DefaultMutableFeatureGate.Enabled(features.ZstdResourceTracker)
	rtGzip := feature.DefaultMutableFeatureGate.Enabled(features.GzipResourceTracker)

	klog.V(2).InfoS("Compression feature gates status",
		"ApplicationRevision.zstd", appRevZstd,
		"ApplicationRevision.gzip", appRevGzip,
		"ResourceTracker.zstd", rtZstd,
		"ResourceTracker.gzip", rtGzip)

	var validationErrors []error

	// Validate ApplicationRevision if compression is enabled
	if appRevZstd || appRevGzip {
		klog.InfoS("ApplicationRevision compression enabled, validating CRD compatibility")
		if err := h.validateApplicationRevisionCRD(ctx, appRevZstd, appRevGzip); err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("ApplicationRevision: %w", err))
		}
	}

	// Validate ResourceTracker if compression is enabled
	if rtZstd || rtGzip {
		klog.InfoS("ResourceTracker compression enabled, validating CRD compatibility")
		if err := h.validateResourceTrackerCRD(ctx, rtZstd, rtGzip); err != nil {
			validationErrors = append(validationErrors, fmt.Errorf("ResourceTracker: %w", err))
		}
	}

	// Check for priority conflicts when multiple compression types are enabled
	if (appRevZstd && appRevGzip) || (rtZstd && rtGzip) {
		klog.V(1).InfoS("Multiple compression types enabled, Zstd will take priority",
			"ApplicationRevision", fmt.Sprintf("zstd=%v, gzip=%v", appRevZstd, appRevGzip),
			"ResourceTracker", fmt.Sprintf("zstd=%v, gzip=%v", rtZstd, rtGzip))
	}

	if len(validationErrors) > 0 {
		klog.ErrorS(nil, "Compression CRD validation failed", "errors", validationErrors)
		return fmt.Errorf("compression CRD validation failed: %v", validationErrors)
	}

	if !appRevZstd && !appRevGzip && !rtZstd && !rtGzip {
		klog.InfoS("No compression features enabled, skipping compression CRD validation")
		return nil
	}

	klog.InfoS("Compression CRD validation completed successfully")
	return nil
}

// validateApplicationRevisionCRD performs a round-trip test to ensure the
// ApplicationRevision CRD supports compression fields
//
// This validation is essential because ApplicationRevision stores snapshots of applications
// and their definitions. With compression enabled, these snapshots can be significantly
// reduced in size (up to 10x compression ratio for large applications).
//
// The function:
// 1. Creates a test ApplicationRevision with compression type set (Zstd or Gzip)
// 2. Stores it in the cluster to test CRD field support
// 3. Retrieves it back to verify data integrity
// 4. Checks that the application name is preserved (indicates proper field storage)
// 5. Cleans up test resources using label selectors
//
// If validation fails, it means the CRD doesn't have the compression field in its schema,
// and enabling compression would result in data loss.
func (h *Hook) validateApplicationRevisionCRD(ctx context.Context, zstdEnabled, gzipEnabled bool) error {
	testName := fmt.Sprintf("apprev-pre-check.%d", time.Now().UnixNano())
	namespace := k8s.GetRuntimeNamespace()

	klog.V(2).InfoS("Creating test ApplicationRevision for CRD validation",
		"name", testName,
		"namespace", namespace)

	appRev := &v1beta1.ApplicationRevision{}
	appRev.Name = testName
	appRev.Namespace = namespace
	appRev.SetLabels(map[string]string{oam.LabelPreCheck: types.VelaCoreName})
	appRev.Spec.Application.Name = testName
	appRev.Spec.Application.Spec.Components = []common.ApplicationComponent{}

	// Set compression type based on enabled features (Zstd takes priority)
	var compressionType compression.Type
	if zstdEnabled {
		compressionType = compression.Zstd
		appRev.Spec.Compression.SetType(compression.Zstd)
		klog.V(3).InfoS("Setting ApplicationRevision compression type", "type", "zstd")
	} else if gzipEnabled {
		compressionType = compression.Gzip
		appRev.Spec.Compression.SetType(compression.Gzip)
		klog.V(3).InfoS("Setting ApplicationRevision compression type", "type", "gzip")
	}

	// Register cleanup function
	defer func() {
		klog.V(2).InfoS("Cleaning up test ApplicationRevisions",
			"namespace", namespace,
			"label", oam.LabelPreCheck)

		if err := h.Client.DeleteAllOf(ctx, &v1beta1.ApplicationRevision{},
			client.InNamespace(namespace),
			client.MatchingLabels{oam.LabelPreCheck: types.VelaCoreName}); err != nil {
			klog.ErrorS(err, "Failed to clean up test ApplicationRevision resources",
				"namespace", namespace)
		} else {
			klog.V(3).InfoS("Successfully cleaned up test ApplicationRevision resources")
		}
	}()

	// Create test resource
	klog.V(2).InfoS("Writing test ApplicationRevision to cluster")
	if err := h.Client.Create(ctx, appRev); err != nil {
		klog.ErrorS(err, "Failed to create test ApplicationRevision",
			"name", testName,
			"namespace", namespace)
		return fmt.Errorf("failed to create test ApplicationRevision: %w", err)
	}
	klog.V(3).InfoS("Test ApplicationRevision created successfully")

	// Read back the resource
	key := client.ObjectKeyFromObject(appRev)
	klog.V(2).InfoS("Reading back test ApplicationRevision from cluster",
		"key", key.String())

	if err := h.Client.Get(ctx, key, appRev); err != nil {
		klog.ErrorS(err, "Failed to read back test ApplicationRevision",
			"key", key.String())
		return fmt.Errorf("failed to read test ApplicationRevision: %w", err)
	}
	klog.V(3).InfoS("Test ApplicationRevision read successfully")

	// Validate round-trip integrity
	klog.V(2).InfoS("Validating ApplicationRevision round-trip data integrity",
		"expectedName", testName,
		"actualName", appRev.Spec.Application.Name)

	if appRev.Spec.Application.Name != testName {
		klog.ErrorS(nil, "ApplicationRevision CRD round-trip validation failed - data corruption detected",
			"expectedName", testName,
			"actualName", appRev.Spec.Application.Name,
			"compressionType", compressionType,
			"issue", "The ApplicationRevision CRD does not support compression fields")
		return fmt.Errorf("the ApplicationRevision CRD is not updated. Compression cannot be used. Please upgrade your CRD to latest ones")
	}

	klog.V(2).InfoS("ApplicationRevision round-trip validation passed - CRD supports compression",
		"compressionType", compressionType)

	return nil
}

// validateResourceTrackerCRD performs a round-trip test to ensure the
// ResourceTracker CRD supports compression fields
//
// ResourceTracker is critical for tracking all resources created by an application.
// It stores the full manifest of each resource, which can be very large for complex
// applications with many components. Compression can reduce storage by 80-90%.
//
// The function:
// 1. Creates a test ResourceTracker with a ManagedResource containing a ConfigMap
// 2. Sets the compression type based on enabled features (Zstd takes priority)
// 3. Stores the ResourceTracker in the cluster (cluster-scoped resource)
// 4. Retrieves it back and validates:
//   - The ManagedResources array is preserved (not empty)
//   - The resource name in ManagedResource matches the original
//
// 5. Cleans up test resources using label selectors
//
// Validation failure indicates the CRD lacks compression support, which would cause:
// - Loss of tracked resources
// - Inability to properly garbage collect resources
// - Broken application lifecycle management
func (h *Hook) validateResourceTrackerCRD(ctx context.Context, zstdEnabled, gzipEnabled bool) error {
	testName := fmt.Sprintf("rt-pre-check.%d", time.Now().UnixNano())
	namespace := k8s.GetRuntimeNamespace() // Used for the ConfigMap reference in ManagedResource

	klog.V(2).InfoS("Creating test ResourceTracker for CRD validation (cluster-scoped)",
		"name", testName)

	rt := &v1beta1.ResourceTracker{}
	rt.Name = testName
	// ResourceTracker is cluster-scoped, no namespace
	rt.SetLabels(map[string]string{
		oam.LabelPreCheck:    types.VelaCoreName,
		oam.LabelAppName:     testName,
		oam.LabelAppRevision: testName + "-v1",
	})

	// Create a test ManagedResource to validate compression
	testResource := v1beta1.ManagedResource{
		ClusterObjectReference: common.ClusterObjectReference{
			Cluster: "local",
			ObjectReference: corev1.ObjectReference{
				APIVersion: "v1",
				Kind:       "ConfigMap",
				Namespace:  namespace,
				Name:       testName,
			},
		},
		OAMObjectReference: common.OAMObjectReference{
			Component: testName + "-comp",
			Trait:     "",
		},
		Data: &runtime.RawExtension{
			Raw: []byte(fmt.Sprintf(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"%s","namespace":"%s"},"data":{"test":"data"}}`, testName, namespace)),
		},
		Deleted: false,
	}

	rt.Spec.ManagedResources = []v1beta1.ManagedResource{testResource}

	// Set compression type based on enabled features (Zstd takes priority)
	var compressionType compression.Type
	if zstdEnabled {
		compressionType = compression.Zstd
		rt.Spec.Compression.SetType(compression.Zstd)
		klog.V(3).InfoS("Setting ResourceTracker compression type", "type", "zstd")
	} else if gzipEnabled {
		compressionType = compression.Gzip
		rt.Spec.Compression.SetType(compression.Gzip)
		klog.V(3).InfoS("Setting ResourceTracker compression type", "type", "gzip")
	}

	// Register cleanup function
	defer func() {
		klog.V(2).InfoS("Cleaning up test ResourceTrackers (cluster-scoped)",
			"label", oam.LabelPreCheck)

		// ResourceTracker is cluster-scoped, so no namespace option
		if err := h.Client.DeleteAllOf(ctx, &v1beta1.ResourceTracker{},
			client.MatchingLabels{oam.LabelPreCheck: types.VelaCoreName}); err != nil {
			klog.ErrorS(err, "Failed to clean up test ResourceTracker resources")
		} else {
			klog.V(3).InfoS("Successfully cleaned up test ResourceTracker resources")
		}
	}()

	// Create test resource
	klog.V(2).InfoS("Writing test ResourceTracker to cluster")
	if err := h.Client.Create(ctx, rt); err != nil {
		klog.ErrorS(err, "Failed to create test ResourceTracker",
			"name", testName)
		return fmt.Errorf("failed to create test ResourceTracker: %w", err)
	}
	klog.V(3).InfoS("Test ResourceTracker created successfully")

	// Read back the resource
	key := client.ObjectKeyFromObject(rt)
	klog.V(2).InfoS("Reading back test ResourceTracker from cluster",
		"key", key.String())

	readRT := &v1beta1.ResourceTracker{}
	if err := h.Client.Get(ctx, key, readRT); err != nil {
		klog.ErrorS(err, "Failed to read back test ResourceTracker",
			"key", key.String())
		return fmt.Errorf("failed to read test ResourceTracker: %w", err)
	}
	klog.V(3).InfoS("Test ResourceTracker read successfully")

	// Validate round-trip integrity - check if ManagedResource data is preserved
	klog.V(2).InfoS("Validating ResourceTracker round-trip data integrity",
		"expectedResources", len(rt.Spec.ManagedResources),
		"actualResources", len(readRT.Spec.ManagedResources))

	if len(readRT.Spec.ManagedResources) != 1 {
		klog.ErrorS(nil, "ResourceTracker CRD round-trip validation failed - managed resources lost",
			"expectedCount", 1,
			"actualCount", len(readRT.Spec.ManagedResources),
			"compressionType", compressionType,
			"issue", "The ResourceTracker CRD does not support compression fields")
		return fmt.Errorf("the ResourceTracker CRD is not updated. Compression cannot be used. Please upgrade your CRD to latest ones")
	}

	// Verify the ManagedResource data is intact
	actualResource := readRT.Spec.ManagedResources[0]
	if actualResource.ClusterObjectReference.Name != testName {
		klog.ErrorS(nil, "ResourceTracker CRD round-trip validation failed - data corruption detected",
			"expectedName", testName,
			"actualName", actualResource.ClusterObjectReference.Name,
			"compressionType", compressionType,
			"issue", "The ResourceTracker CRD does not properly support compression")
		return fmt.Errorf("the ResourceTracker CRD compression validation failed. Please upgrade your CRD to latest ones")
	}

	klog.V(2).InfoS("ResourceTracker round-trip validation passed - CRD supports compression",
		"compressionType", compressionType)

	return nil
}
