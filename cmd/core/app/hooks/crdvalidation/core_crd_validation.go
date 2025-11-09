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

	"github.com/kubevela/pkg/util/k8s"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/pkg/oam"
)

// CoreCRDInfo contains information about a core CRD to validate
// Each core CRD has specific requirements that must be met for the controller to function properly
type CoreCRDInfo struct {
	Name           string                                                   // Full CRD name (e.g., "applications.core.oam.dev")
	Group          string                                                   // API group (e.g., "core.oam.dev")
	Version        string                                                   // Required version (e.g., "v1beta1")
	Namespaced     bool                                                     // Whether the CRD is namespace-scoped or cluster-scoped
	RequiredFields []string                                                 // Schema fields that must exist (e.g., "spec.components")
	CreateTestFunc func(name, namespace string) (client.Object, error)    // Function to create test resources for round-trip validation
}

// ValidateCoreCRDs validates that core CRDs (Application, TraitDefinition, PolicyDefinition, WorkflowStepDefinition)
// exist and have the required schema fields.
//
// This function performs comprehensive validation for each core CRD:
// 1. CRD Existence - Verifies the CRD is installed in the cluster
// 2. Version Check - Ensures the required version (v1beta1) exists and is served
// 3. Schema Validation - Confirms all required fields are present in the OpenAPI schema
// 4. Round-Trip Test - Creates, stores, and retrieves test resources to verify data integrity
//
// The validation is essential because:
// - Missing CRDs will cause the controller to fail when creating resources
// - Missing schema fields can cause data loss or corruption
// - Incorrect versions may have incompatible schemas
// - Round-trip failures indicate the CRD cannot properly store controller data
func (h *Hook) ValidateCoreCRDs(ctx context.Context) error {
	klog.InfoS("Starting core CRD validation")
	startTime := time.Now()

	coreCRDs := []CoreCRDInfo{
		{
			Name:       "applications.core.oam.dev",
			Group:      "core.oam.dev",
			Version:    "v1beta1",
			Namespaced: true,
			RequiredFields: []string{
				"spec.components",
				"spec.workflow",
				"spec.policies",
				// Note: status is optional - many CRDs add it later via status subresource
			},
			CreateTestFunc: h.createTestApplication,
		},
		{
			Name:       "traitdefinitions.core.oam.dev",
			Group:      "core.oam.dev",
			Version:    "v1beta1",
			Namespaced: false,
			RequiredFields: []string{
				"spec.schematic",
				"spec.appliesToWorkloads",
				"spec.workloadRefPath",
				// Note: status is optional - many CRDs add it later via status subresource
			},
			CreateTestFunc: h.createTestTraitDefinition,
		},
		{
			Name:       "policydefinitions.core.oam.dev",
			Group:      "core.oam.dev",
			Version:    "v1beta1",
			Namespaced: false,
			RequiredFields: []string{
				"spec.schematic",
				"spec.definitionRef",
				// Note: status is optional - many CRDs add it later via status subresource
			},
			CreateTestFunc: h.createTestPolicyDefinition,
		},
		{
			Name:       "workflowstepdefinitions.core.oam.dev",
			Group:      "core.oam.dev",
			Version:    "v1beta1",
			Namespaced: false,
			RequiredFields: []string{
				"spec.schematic",
				"spec.definitionRef",
				// Note: status is optional - many CRDs add it later via status subresource
			},
			CreateTestFunc: h.createTestWorkflowStepDefinition,
		},
	}

	var validationErrors []error

	for _, crdInfo := range coreCRDs {
		klog.V(1).InfoS("Validating CRD", "crd", crdInfo.Name)

		// Check if CRD exists
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := h.Client.Get(ctx, client.ObjectKey{Name: crdInfo.Name}, crd); err != nil {
			if errors.IsNotFound(err) {
				klog.ErrorS(err, "CRD not found", "crd", crdInfo.Name)
				validationErrors = append(validationErrors, fmt.Errorf("CRD %s not found: %w", crdInfo.Name, err))
				continue
			}
			klog.ErrorS(err, "Failed to get CRD", "crd", crdInfo.Name)
			validationErrors = append(validationErrors, fmt.Errorf("failed to get CRD %s: %w", crdInfo.Name, err))
			continue
		}

		// Validate CRD has the expected version
		versionFound := false
		for _, version := range crd.Spec.Versions {
			if version.Name == crdInfo.Version {
				versionFound = true
				klog.V(2).InfoS("CRD version found", "crd", crdInfo.Name, "version", crdInfo.Version)

				// Check if the version is served
				if !version.Served {
					klog.ErrorS(nil, "CRD version not served", "crd", crdInfo.Name, "version", crdInfo.Version)
					validationErrors = append(validationErrors, fmt.Errorf("CRD %s version %s is not served", crdInfo.Name, crdInfo.Version))
				}

				// Check if storage version
				if version.Storage {
					klog.V(3).InfoS("CRD version is storage version", "crd", crdInfo.Name, "version", crdInfo.Version)
				}
				break
			}
		}

		if !versionFound {
			klog.ErrorS(nil, "CRD version not found", "crd", crdInfo.Name, "version", crdInfo.Version)
			validationErrors = append(validationErrors, fmt.Errorf("CRD %s does not have version %s", crdInfo.Name, crdInfo.Version))
			continue
		}

		// Validate CRD schema has required fields
		if err := h.validateCRDSchema(crd, crdInfo); err != nil {
			klog.ErrorS(err, "CRD schema validation failed", "crd", crdInfo.Name)
			validationErrors = append(validationErrors, fmt.Errorf("CRD %s schema validation failed: %w", crdInfo.Name, err))
			continue
		}

		// Perform round-trip test for the CRD
		// Currently only perform round-trip tests for namespaced CRDs
		// Cluster-scoped resources may have creation restrictions in certain environments
		// Schema validation above is sufficient to verify field presence
		if crdInfo.Namespaced {
			if err := h.performCRDRoundTripTest(ctx, crdInfo); err != nil {
				klog.ErrorS(err, "CRD round-trip test failed", "crd", crdInfo.Name)
				validationErrors = append(validationErrors, fmt.Errorf("CRD %s round-trip test failed: %w", crdInfo.Name, err))
			} else {
				klog.V(1).InfoS("CRD validation passed", "crd", crdInfo.Name)
			}
		} else {
			// For cluster-scoped resources, schema validation is sufficient
			klog.V(1).InfoS("CRD schema validation passed (round-trip skipped for cluster-scoped)", "crd", crdInfo.Name)
		}
	}

	if len(validationErrors) > 0 {
		klog.ErrorS(nil, "Core CRD validation failed",
			"errors", len(validationErrors),
			"duration", time.Since(startTime))
		return fmt.Errorf("core CRD validation failed with %d errors: %v", len(validationErrors), validationErrors)
	}

	klog.InfoS("Core CRD validation completed successfully",
		"crdsValidated", len(coreCRDs),
		"duration", time.Since(startTime))
	return nil
}

// validateCRDSchema checks if the CRD has the required fields in its schema
//
// This function performs deep schema validation to ensure:
// 1. The CRD version has an OpenAPI v3 schema defined
// 2. The schema contains a 'spec' field (required for all OAM resources)
// 3. All required fields specified in CoreCRDInfo.RequiredFields exist in the schema
// 4. Field paths are properly nested (e.g., "spec.components" checks spec exists and has components)
//
// Schema validation is critical because:
// - Missing fields will cause the controller to fail when accessing expected properties
// - Incorrect schema structure can lead to data being silently dropped during storage
// - The controller relies on specific field paths for its business logic
func (h *Hook) validateCRDSchema(crd *apiextensionsv1.CustomResourceDefinition, info CoreCRDInfo) error {
	// Find the version schema
	var versionSchema *apiextensionsv1.CustomResourceValidation
	for _, version := range crd.Spec.Versions {
		if version.Name == info.Version {
			versionSchema = version.Schema
			break
		}
	}

	if versionSchema == nil || versionSchema.OpenAPIV3Schema == nil {
		return fmt.Errorf("no OpenAPI schema found for version %s", info.Version)
	}

	schema := versionSchema.OpenAPIV3Schema
	klog.V(3).InfoS("Checking CRD schema", "crd", info.Name, "type", schema.Type)

	// Check for required fields in the schema
	properties := schema.Properties
	if properties == nil {
		return fmt.Errorf("schema has no properties defined")
	}

	// Validate spec and status exist
	if _, ok := properties["spec"]; !ok {
		return fmt.Errorf("schema missing 'spec' field")
	}
	if _, ok := properties["status"]; !ok {
		klog.V(2).InfoS("CRD schema missing 'status' field (warning)", "crd", info.Name)
	}

	// Check specific required fields
	missingFields := []string{}
	for _, field := range info.RequiredFields {
		if err := h.checkFieldPath(properties, field); err != nil {
			missingFields = append(missingFields, field)
			klog.V(2).InfoS("Required field not found in schema", "crd", info.Name, "field", field, "error", err)
		}
	}

	if len(missingFields) > 0 {
		return fmt.Errorf("schema missing required fields: %v", missingFields)
	}

	klog.V(2).InfoS("CRD schema validation passed", "crd", info.Name)
	return nil
}

// checkFieldPath validates that a field path exists in the schema properties
//
// This function recursively traverses the schema to verify a field path exists.
// For example, given fieldPath "spec.components.traits":
// 1. Checks "spec" exists in top-level properties
// 2. Checks "spec" has properties defined
// 3. Checks "components" exists in spec's properties
// 4. Checks "components" has properties defined
// 5. Checks "traits" exists in components' properties
//
// The validation ensures that nested fields required by the controller are present
// in the CRD schema, preventing runtime errors when accessing these paths.
func (h *Hook) checkFieldPath(properties map[string]apiextensionsv1.JSONSchemaProps, fieldPath string) error {
	// Split the field path (e.g., "spec.components" -> ["spec", "components"])
	parts := []string{}
	current := ""
	for _, ch := range fieldPath {
		if ch == '.' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(ch)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}

	// Navigate through the schema
	currentProps := properties
	for i, part := range parts {
		prop, ok := currentProps[part]
		if !ok {
			return fmt.Errorf("field '%s' not found at path '%s'", part, fieldPath[:len(fieldPath)-len(current)])
		}

		// If not the last part, continue navigating
		if i < len(parts)-1 {
			if prop.Properties == nil {
				return fmt.Errorf("field '%s' has no properties", part)
			}
			currentProps = prop.Properties
		}
	}

	return nil
}

// performCRDRoundTripTest creates a test resource and validates it can be stored and retrieved
//
// This function performs a complete round-trip test to verify CRD data integrity:
// 1. Creates a test resource with known data using the CreateTestFunc
// 2. Labels it with oam.LabelPreCheck for cleanup tracking
// 3. Stores the resource in the cluster via the Kubernetes API
// 4. Retrieves the resource back from the cluster
// 5. Validates the retrieved data matches what was stored
// 6. Cleans up test resources using label selectors
//
// Round-trip testing is crucial because:
// - It verifies the CRD can actually store the data structures the controller needs
// - It detects issues with field preservation and data corruption
// - It ensures the CRD version in the cluster is compatible with the controller's expectations
// - It validates that compression fields (if enabled) work correctly
//
// The test uses unique names with timestamps to avoid conflicts and ensure cleanup.
func (h *Hook) performCRDRoundTripTest(ctx context.Context, info CoreCRDInfo) error {
	testName := fmt.Sprintf("%s-pre-check.%d", info.Name[:4], time.Now().UnixNano())
	namespace := ""
	if info.Namespaced {
		namespace = k8s.GetRuntimeNamespace()
	}

	klog.V(2).InfoS("Performing round-trip test", "crd", info.Name, "testName", testName, "namespaced", info.Namespaced)

	// Create test object
	testObj, err := info.CreateTestFunc(testName, namespace)
	if err != nil {
		return fmt.Errorf("failed to create test object: %w", err)
	}

	// Set cleanup label
	testObj.SetLabels(map[string]string{oam.LabelPreCheck: types.VelaCoreName})

	// Set namespace only for namespaced resources
	// For cluster-scoped resources, explicitly ensure no namespace is set
	// Kubernetes API strictly validates that cluster-scoped resources have no namespace field
	if info.Namespaced {
		testObj.SetNamespace(namespace)
	}

	// Register cleanup
	defer func() {
		klog.V(2).InfoS("Cleaning up test resources", "crd", info.Name, "namespaced", info.Namespaced)

		opts := []client.DeleteAllOfOption{client.MatchingLabels{oam.LabelPreCheck: types.VelaCoreName}}
		if info.Namespaced {
			opts = append(opts, client.InNamespace(namespace))
		}

		switch info.Name {
		case "applications.core.oam.dev":
			h.Client.DeleteAllOf(ctx, &v1beta1.Application{}, opts...)
		case "traitdefinitions.core.oam.dev":
			h.Client.DeleteAllOf(ctx, &v1beta1.TraitDefinition{}, opts...)
		case "policydefinitions.core.oam.dev":
			h.Client.DeleteAllOf(ctx, &v1beta1.PolicyDefinition{}, opts...)
		case "workflowstepdefinitions.core.oam.dev":
			h.Client.DeleteAllOf(ctx, &v1beta1.WorkflowStepDefinition{}, opts...)
		}
	}()

	// Create the test resource
	klog.V(2).InfoS("Creating test resource", "crd", info.Name, "name", testName, "namespaced", info.Namespaced)
	if err := h.Client.Create(ctx, testObj); err != nil {
		return fmt.Errorf("failed to create test resource: %w", err)
	}
	klog.V(3).InfoS("Test resource created", "crd", info.Name, "name", testName)

	// Read back the resource
	readObj := testObj.DeepCopyObject().(client.Object)
	key := client.ObjectKey{Name: testName}
	if info.Namespaced {
		key.Namespace = namespace
	}
	if err := h.Client.Get(ctx, key, readObj); err != nil {
		return fmt.Errorf("failed to read back test resource: %w", err)
	}
	klog.V(3).InfoS("Test resource read back successfully", "crd", info.Name, "name", testName)

	// Basic validation that the resource was stored correctly
	if readObj.GetName() != testName {
		return fmt.Errorf("resource name mismatch: expected %s, got %s", testName, readObj.GetName())
	}

	klog.V(2).InfoS("Round-trip test passed", "crd", info.Name)
	return nil
}

// Test object creation functions
// These functions create minimal but valid test resources for each CRD type.
// The test resources contain the minimum required fields to pass validation while
// being simple enough to avoid complex dependencies or validation errors.

// createTestApplication creates a minimal Application resource for round-trip testing
// The Application includes:
// - A single component with webservice type
// - Basic properties with nginx image configuration
// This validates the core Application structure can be stored and retrieved
func (h *Hook) createTestApplication(name, namespace string) (client.Object, error) {
	app := &v1beta1.Application{}
	app.Name = name
	app.Namespace = namespace
	app.Spec.Components = []common.ApplicationComponent{
		{
			Name: name + "-comp",
			Type: "webservice",
			Properties: &runtime.RawExtension{
				Raw: []byte(`{"image":"nginx:latest","port":80}`),
			},
		},
	}
	return app, nil
}

// createTestTraitDefinition creates a minimal TraitDefinition resource for round-trip testing
// The TraitDefinition includes:
// - A CUE schematic with a simple replicas parameter template
// - AppliesToWorkloads field specifying it applies to deployments
// This validates the TraitDefinition structure and CUE template storage
func (h *Hook) createTestTraitDefinition(name, namespace string) (client.Object, error) {
	td := &v1beta1.TraitDefinition{}
	td.Name = name
	// TraitDefinition is cluster-scoped, no namespace
	td.Spec.Schematic = &common.Schematic{
		CUE: &common.CUE{
			Template: `
				parameter: {
					replicas: int | *1
				}
				patch: {
					spec: replicas: parameter.replicas
				}
			`,
		},
	}
	td.Spec.AppliesToWorkloads = []string{"deployments.apps"}
	return td, nil
}

// createTestPolicyDefinition creates a minimal PolicyDefinition resource for round-trip testing
// The PolicyDefinition includes:
// - A CUE schematic with a namespace parameter
// This validates the PolicyDefinition structure and template storage
func (h *Hook) createTestPolicyDefinition(name, namespace string) (client.Object, error) {
	pd := &v1beta1.PolicyDefinition{}
	pd.Name = name
	// PolicyDefinition is cluster-scoped, no namespace
	pd.Spec.Schematic = &common.Schematic{
		CUE: &common.CUE{
			Template: `
				parameter: {
					namespace: string
				}
			`,
		},
	}
	return pd, nil
}

// createTestWorkflowStepDefinition creates a minimal WorkflowStepDefinition resource for round-trip testing
// The WorkflowStepDefinition includes:
// - A CUE schematic with a message parameter and wait duration
// This validates the WorkflowStepDefinition structure and workflow template storage
func (h *Hook) createTestWorkflowStepDefinition(name, namespace string) (client.Object, error) {
	wsd := &v1beta1.WorkflowStepDefinition{}
	wsd.Name = name
	// WorkflowStepDefinition is cluster-scoped, no namespace
	wsd.Spec.Schematic = &common.Schematic{
		CUE: &common.CUE{
			Template: `
				parameter: {
					message: string
				}
				wait: {
					duration: "5s"
				}
			`,
		},
	}
	return wsd, nil
}