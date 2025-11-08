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

package schema

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kubevela/pkg/util/singleton"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oam-dev/kubevela/cmd/core/app/hooks"
)

// SchemaValidator validates CRD schemas for completeness and best practices
type SchemaValidator struct {
	client.Client
}

// NewHook creates a new schema validation hook
func NewHook() hooks.PreStartHook {
	klog.V(3).InfoS("Initializing CRD schema validation hook")
	return &SchemaValidator{Client: singleton.KubeClient.Get()}
}

// Name returns the hook name for logging
func (v *SchemaValidator) Name() string {
	return "CRDSchemaValidation"
}

// CRDSchemaCheck defines schema validation requirements for a CRD
type CRDSchemaCheck struct {
	Name                      string
	CheckPreserveUnknownFields bool
	RequireOpenAPISchema      bool
	CheckPrinterColumns       bool
	CheckSubresources         bool
	WarnOnly                  bool
}

// Run executes the CRD schema validation
func (v *SchemaValidator) Run(ctx context.Context) error {
	klog.InfoS("Starting CRD schema validation")
	startTime := time.Now()

	// Define CRDs to validate
	crdChecks := []CRDSchemaCheck{
		{
			Name:                      "applications.core.oam.dev",
			RequireOpenAPISchema:      true,
			CheckPrinterColumns:       true,
			CheckSubresources:         true,
			CheckPreserveUnknownFields: true,
		},
		{
			Name:                      "applicationrevisions.core.oam.dev",
			RequireOpenAPISchema:      true,
			CheckPreserveUnknownFields: true,
			CheckSubresources:         true,
		},
		{
			Name:                      "resourcetrackers.core.oam.dev",
			RequireOpenAPISchema:      true,
			CheckPreserveUnknownFields: true,
			CheckSubresources:         false, // ResourceTracker doesn't need status subresource
		},
		{
			Name:                      "definitionrevisions.core.oam.dev",
			RequireOpenAPISchema:      true,
			CheckPreserveUnknownFields: true,
			CheckSubresources:         true,
		},
		{
			Name:                      "componentdefinitions.core.oam.dev",
			RequireOpenAPISchema:      true,
			CheckPreserveUnknownFields: true,
			CheckSubresources:         true,
		},
		{
			Name:                      "traitdefinitions.core.oam.dev",
			RequireOpenAPISchema:      true,
			CheckPreserveUnknownFields: true,
			CheckSubresources:         true,
		},
		{
			Name:                      "policydefinitions.core.oam.dev",
			RequireOpenAPISchema:      true,
			CheckPreserveUnknownFields: true,
			CheckSubresources:         true,
		},
		{
			Name:                      "workflowstepdefinitions.core.oam.dev",
			RequireOpenAPISchema:      true,
			CheckPreserveUnknownFields: true,
			CheckSubresources:         true,
		},
		{
			Name:                      "workloaddefinitions.core.oam.dev",
			RequireOpenAPISchema:      true,
			CheckPreserveUnknownFields: true,
			CheckSubresources:         true,
		},
		{
			Name:                 "workflows.core.oam.dev",
			RequireOpenAPISchema: true,
			CheckSubresources:    true,
			WarnOnly:             true, // Less critical CRD
		},
		{
			Name:                 "policies.core.oam.dev",
			RequireOpenAPISchema: true,
			WarnOnly:             true, // Less critical CRD
		},
	}

	var validationErrors []error
	var warnings []string
	missingCRDs := []string{}

	for _, check := range crdChecks {
		klog.V(1).InfoS("Validating CRD schema", "crd", check.Name)

		// Get the CRD
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := v.Client.Get(ctx, client.ObjectKey{Name: check.Name}, crd); err != nil {
			if errors.IsNotFound(err) {
				msg := fmt.Sprintf("CRD %s not found", check.Name)
				if check.WarnOnly {
					klog.V(1).InfoS(msg)
					warnings = append(warnings, msg)
				} else {
					klog.ErrorS(err, msg)
					missingCRDs = append(missingCRDs, check.Name)
				}
				continue
			}
			klog.ErrorS(err, "Failed to get CRD", "crd", check.Name)
			validationErrors = append(validationErrors, fmt.Errorf("failed to get CRD %s: %w", check.Name, err))
			continue
		}

		// Perform schema checks
		schemaIssues := v.validateSchema(crd, check)
		if len(schemaIssues) > 0 {
			if check.WarnOnly {
				for _, issue := range schemaIssues {
					klog.V(1).InfoS("CRD schema issue", "crd", check.Name, "issue", issue)
					warnings = append(warnings, fmt.Sprintf("%s: %s", check.Name, issue))
				}
			} else {
				for _, issue := range schemaIssues {
					klog.ErrorS(nil, "CRD schema issue", "crd", check.Name, "issue", issue)
					validationErrors = append(validationErrors, fmt.Errorf("CRD %s: %s", check.Name, issue))
				}
			}
		} else {
			klog.V(2).InfoS("CRD schema validation passed", "crd", check.Name)
		}
	}

	// Report summary
	if len(missingCRDs) > 0 {
		klog.ErrorS(nil, "Missing critical CRDs", "crds", missingCRDs)
		validationErrors = append(validationErrors,
			fmt.Errorf("missing critical CRDs: %s", strings.Join(missingCRDs, ", ")))
	}

	if len(warnings) > 0 {
		klog.InfoS("CRD schema validation completed with warnings",
			"warnings", len(warnings),
			"duration", time.Since(startTime))
	}

	if len(validationErrors) > 0 {
		klog.ErrorS(nil, "CRD schema validation failed",
			"errors", len(validationErrors),
			"duration", time.Since(startTime))
		return fmt.Errorf("CRD schema validation failed with %d errors: %v",
			len(validationErrors), validationErrors)
	}

	klog.InfoS("CRD schema validation completed successfully",
		"crdsValidated", len(crdChecks),
		"duration", time.Since(startTime))
	return nil
}

// validateSchema performs specific schema checks on a CRD
func (v *SchemaValidator) validateSchema(crd *apiextensionsv1.CustomResourceDefinition, check CRDSchemaCheck) []string {
	issues := []string{}

	// Check for at least one served version
	hasServedVersion := false
	hasStorageVersion := false
	for _, version := range crd.Spec.Versions {
		if version.Served {
			hasServedVersion = true
		}
		if version.Storage {
			hasStorageVersion = true
		}

		// Check OpenAPI schema
		if check.RequireOpenAPISchema {
			if version.Schema == nil || version.Schema.OpenAPIV3Schema == nil {
				issues = append(issues, fmt.Sprintf("version %s missing OpenAPI v3 schema", version.Name))
			} else {
				// Check schema completeness
				schema := version.Schema.OpenAPIV3Schema
				if schema.Type == "" {
					issues = append(issues, fmt.Sprintf("version %s schema missing type field", version.Name))
				}
				if schema.Properties == nil || len(schema.Properties) == 0 {
					issues = append(issues, fmt.Sprintf("version %s schema has no properties defined", version.Name))
				}
			}
		}
	}

	if !hasServedVersion {
		issues = append(issues, "no served version found")
	}
	if !hasStorageVersion {
		issues = append(issues, "no storage version defined")
	}

	// Check PreserveUnknownFields
	if check.CheckPreserveUnknownFields {
		if crd.Spec.PreserveUnknownFields {
			klog.V(2).InfoS("CRD has global PreserveUnknownFields enabled",
				"crd", check.Name,
				"recommendation", "Consider using per-field preservation instead")
		}

		// Check for PreserveUnknownFields in schema
		for _, version := range crd.Spec.Versions {
			if version.Schema != nil && version.Schema.OpenAPIV3Schema != nil {
				preserveCount := v.countPreserveUnknownFields(version.Schema.OpenAPIV3Schema)
				if preserveCount > 0 {
					klog.V(2).InfoS("CRD uses PreserveUnknownFields markers",
						"crd", check.Name,
						"version", version.Name,
						"count", preserveCount)
				}
			}
		}
	}

	// Check printer columns
	if check.CheckPrinterColumns {
		hasAgeColumn := false
		for _, version := range crd.Spec.Versions {
			if version.AdditionalPrinterColumns != nil {
				for _, col := range version.AdditionalPrinterColumns {
					if col.Name == "AGE" || col.Type == "date" {
						hasAgeColumn = true
					}
				}
			}
		}
		if !hasAgeColumn {
			klog.V(3).InfoS("CRD missing AGE printer column", "crd", check.Name)
		}
	}

	// Check subresources
	if check.CheckSubresources {
		for _, version := range crd.Spec.Versions {
			if version.Subresources == nil || version.Subresources.Status == nil {
				klog.V(2).InfoS("CRD version missing status subresource",
					"crd", check.Name,
					"version", version.Name)
			}
		}
	}

	// Check for deprecated versions
	for _, version := range crd.Spec.Versions {
		if version.Deprecated {
			klog.V(1).InfoS("CRD has deprecated version",
				"crd", check.Name,
				"version", version.Name,
				"deprecationWarning", version.DeprecationWarning)
		}
	}

	return issues
}

// countPreserveUnknownFields recursively counts PreserveUnknownFields markers in schema
func (v *SchemaValidator) countPreserveUnknownFields(schema *apiextensionsv1.JSONSchemaProps) int {
	count := 0

	if schema.XPreserveUnknownFields != nil && *schema.XPreserveUnknownFields {
		count++
	}

	// Recursively check properties
	if schema.Properties != nil {
		for _, prop := range schema.Properties {
			count += v.countPreserveUnknownFields(&prop)
		}
	}

	// Check items (for arrays)
	if schema.Items != nil {
		if schema.Items.Schema != nil {
			count += v.countPreserveUnknownFields(schema.Items.Schema)
		}
		for _, item := range schema.Items.JSONSchemas {
			count += v.countPreserveUnknownFields(&item)
		}
	}

	// Check additional properties
	if schema.AdditionalProperties != nil && schema.AdditionalProperties.Schema != nil {
		count += v.countPreserveUnknownFields(schema.AdditionalProperties.Schema)
	}

	return count
}