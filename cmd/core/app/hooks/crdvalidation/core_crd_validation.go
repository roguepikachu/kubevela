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
type CoreCRDInfo struct {
	Name           string
	Group          string
	Version        string
	RequiredFields []string
	CreateTestFunc func(name, namespace string) (client.Object, error)
}

// ValidateCoreCRDs validates that core CRDs (Application, TraitDefinition, PolicyDefinition, WorkflowStepDefinition)
// exist and have the required schema fields
func (h *Hook) ValidateCoreCRDs(ctx context.Context) error {
	klog.InfoS("Starting core CRD validation")
	startTime := time.Now()

	coreCRDs := []CoreCRDInfo{
		{
			Name:    "applications.core.oam.dev",
			Group:   "core.oam.dev",
			Version: "v1beta1",
			RequiredFields: []string{
				"spec.components",
				"spec.workflow",
				"spec.policies",
				"status",
			},
			CreateTestFunc: h.createTestApplication,
		},
		{
			Name:    "traitdefinitions.core.oam.dev",
			Group:   "core.oam.dev",
			Version: "v1beta1",
			RequiredFields: []string{
				"spec.schematic",
				"spec.appliesToWorkloads",
				"spec.workloadRefPath",
				"status",
			},
			CreateTestFunc: h.createTestTraitDefinition,
		},
		{
			Name:    "policydefinitions.core.oam.dev",
			Group:   "core.oam.dev",
			Version: "v1beta1",
			RequiredFields: []string{
				"spec.schematic",
				"spec.definitionRef",
				"status",
			},
			CreateTestFunc: h.createTestPolicyDefinition,
		},
		{
			Name:    "workflowstepdefinitions.core.oam.dev",
			Group:   "core.oam.dev",
			Version: "v1beta1",
			RequiredFields: []string{
				"spec.schematic",
				"spec.reference",
				"status",
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
		if err := h.performCRDRoundTripTest(ctx, crdInfo); err != nil {
			klog.ErrorS(err, "CRD round-trip test failed", "crd", crdInfo.Name)
			validationErrors = append(validationErrors, fmt.Errorf("CRD %s round-trip test failed: %w", crdInfo.Name, err))
		} else {
			klog.V(1).InfoS("CRD validation passed", "crd", crdInfo.Name)
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
func (h *Hook) performCRDRoundTripTest(ctx context.Context, info CoreCRDInfo) error {
	testName := fmt.Sprintf("%s-pre-check.%d", info.Name[:4], time.Now().UnixNano())
	namespace := k8s.GetRuntimeNamespace()

	klog.V(2).InfoS("Performing round-trip test", "crd", info.Name, "testName", testName)

	// Create test object
	testObj, err := info.CreateTestFunc(testName, namespace)
	if err != nil {
		return fmt.Errorf("failed to create test object: %w", err)
	}

	// Set cleanup label
	testObj.SetLabels(map[string]string{oam.LabelPreCheck: types.VelaCoreName})

	// Register cleanup
	defer func() {
		klog.V(2).InfoS("Cleaning up test resources", "crd", info.Name, "namespace", namespace)

		switch info.Name {
		case "applications.core.oam.dev":
			h.Client.DeleteAllOf(ctx, &v1beta1.Application{},
				client.InNamespace(namespace),
				client.MatchingLabels{oam.LabelPreCheck: types.VelaCoreName})
		case "traitdefinitions.core.oam.dev":
			h.Client.DeleteAllOf(ctx, &v1beta1.TraitDefinition{},
				client.InNamespace(namespace),
				client.MatchingLabels{oam.LabelPreCheck: types.VelaCoreName})
		case "policydefinitions.core.oam.dev":
			h.Client.DeleteAllOf(ctx, &v1beta1.PolicyDefinition{},
				client.InNamespace(namespace),
				client.MatchingLabels{oam.LabelPreCheck: types.VelaCoreName})
		case "workflowstepdefinitions.core.oam.dev":
			h.Client.DeleteAllOf(ctx, &v1beta1.WorkflowStepDefinition{},
				client.InNamespace(namespace),
				client.MatchingLabels{oam.LabelPreCheck: types.VelaCoreName})
		}
	}()

	// Create the test resource
	if err := h.Client.Create(ctx, testObj); err != nil {
		return fmt.Errorf("failed to create test resource: %w", err)
	}
	klog.V(3).InfoS("Test resource created", "crd", info.Name, "name", testName)

	// Read back the resource
	readObj := testObj.DeepCopyObject().(client.Object)
	key := client.ObjectKey{Name: testName, Namespace: namespace}
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

func (h *Hook) createTestTraitDefinition(name, namespace string) (client.Object, error) {
	td := &v1beta1.TraitDefinition{}
	td.Name = name
	td.Namespace = namespace
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

func (h *Hook) createTestPolicyDefinition(name, namespace string) (client.Object, error) {
	pd := &v1beta1.PolicyDefinition{}
	pd.Name = name
	pd.Namespace = namespace
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

func (h *Hook) createTestWorkflowStepDefinition(name, namespace string) (client.Object, error) {
	wsd := &v1beta1.WorkflowStepDefinition{}
	wsd.Name = name
	wsd.Namespace = namespace
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