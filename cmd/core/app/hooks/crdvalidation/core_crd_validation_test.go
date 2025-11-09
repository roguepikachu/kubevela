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

package crdvalidation_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/cmd/core/app/hooks/crdvalidation"
)

// Test suite is defined in crd_validation_test.go
// All tests in this file run under the main CRD Validation Hook Suite

var _ = Describe("Core CRD Validation", func() {
	var (
		ctx    context.Context
		scheme *runtime.Scheme
		hook   *crdvalidation.Hook
	)

	BeforeEach(func() {
		ctx = context.Background()

		// Create scheme with all required types
		scheme = runtime.NewScheme()
		Expect(apiextensionsv1.AddToScheme(scheme)).Should(Succeed())
		Expect(v1beta1.AddToScheme(scheme)).Should(Succeed())
	})

	// Test Scenario 1: All CRDs exist with proper schemas
	Context("when all core CRDs exist with valid schemas", func() {
		It("should validate successfully", func() {
			// Create valid CRDs for all core resources
			validCRDs := createValidCoreCRDs()

			// Create fake client with all valid CRDs
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(validCRDs...).
				Build()

			hook = &crdvalidation.Hook{
				Client: fakeClient,
			}

			err := hook.ValidateCoreCRDs(ctx)
			Expect(err).Should(Succeed(), "All core CRDs are valid, validation should pass")
		})
	})

	// Test Scenario 2: Missing Application CRD
	Context("when Application CRD is missing", func() {
		It("should fail with appropriate error", func() {
			// Create all CRDs except Application
			crdList := createValidCoreCRDs()
			filteredCRDs := []client.Object{}
			for _, crd := range crdList {
				if crd.GetName() != "applications.core.oam.dev" {
					filteredCRDs = append(filteredCRDs, crd)
				}
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(filteredCRDs...).
				Build()

			hook = &crdvalidation.Hook{
				Client: fakeClient,
			}

			err := hook.ValidateCoreCRDs(ctx)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("applications.core.oam.dev"))
			Expect(err.Error()).Should(ContainSubstring("not found"))
		})
	})

	// Test Scenario 3: CRD exists but wrong version
	Context("when CRD exists but with wrong version", func() {
		It("should fail when v1beta1 version is missing", func() {
			// Create Application CRD with only v1alpha2 version
			wrongVersionCRD := createCRDWithWrongVersion("applications.core.oam.dev", "v1alpha2")
			otherCRDs := createValidCoreCRDs()[1:] // Skip the first one (Application)

			allCRDs := append([]client.Object{wrongVersionCRD}, otherCRDs...)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(allCRDs...).
				Build()

			hook = &crdvalidation.Hook{
				Client: fakeClient,
			}

			err := hook.ValidateCoreCRDs(ctx)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("applications.core.oam.dev"))
			Expect(err.Error()).Should(ContainSubstring("does not have version v1beta1"))
		})

		It("should fail when version exists but is not served", func() {
			// Create Application CRD with v1beta1 but not served
			unservedCRD := createCRDWithUnservedVersion("applications.core.oam.dev")
			otherCRDs := createValidCoreCRDs()[1:]

			allCRDs := append([]client.Object{unservedCRD}, otherCRDs...)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(allCRDs...).
				Build()

			hook = &crdvalidation.Hook{
				Client: fakeClient,
			}

			err := hook.ValidateCoreCRDs(ctx)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("applications.core.oam.dev"))
			Expect(err.Error()).Should(ContainSubstring("version v1beta1 is not served"))
		})
	})

	// Test Scenario 4: CRD missing required fields in schema
	Context("when CRD schema is missing required fields", func() {
		It("should fail when spec.components field is missing from Application CRD", func() {
			// Create Application CRD without spec.components field
			missingFieldCRD := createCRDWithMissingField("applications.core.oam.dev", "spec.components")
			otherCRDs := createValidCoreCRDs()[1:]

			allCRDs := append([]client.Object{missingFieldCRD}, otherCRDs...)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(allCRDs...).
				Build()

			hook = &crdvalidation.Hook{
				Client: fakeClient,
			}

			err := hook.ValidateCoreCRDs(ctx)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("applications.core.oam.dev"))
			Expect(err.Error()).Should(ContainSubstring("schema validation failed"))
			Expect(err.Error()).Should(ContainSubstring("spec.components"))
		})

		It("should fail when spec field itself is missing", func() {
			// Create CRD without spec field at all
			noSpecCRD := createCRDWithoutSpec("traitdefinitions.core.oam.dev")
			otherCRDs := append([]client.Object{createValidCoreCRDs()[0]}, createValidCoreCRDs()[2:]...)

			allCRDs := append([]client.Object{noSpecCRD}, otherCRDs...)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(allCRDs...).
				Build()

			hook = &crdvalidation.Hook{
				Client: fakeClient,
			}

			err := hook.ValidateCoreCRDs(ctx)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("traitdefinitions.core.oam.dev"))
			Expect(err.Error()).Should(ContainSubstring("missing 'spec' field"))
		})
	})

	// Test Scenario 5: CRD without OpenAPI schema
	Context("when CRD has no OpenAPI v3 schema", func() {
		It("should fail validation", func() {
			// Create CRD without OpenAPI schema
			noSchemaCRD := createCRDWithoutSchema("policydefinitions.core.oam.dev")
			otherCRDs := append(createValidCoreCRDs()[:2], createValidCoreCRDs()[3:]...)

			allCRDs := append([]client.Object{noSchemaCRD}, otherCRDs...)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(allCRDs...).
				Build()

			hook = &crdvalidation.Hook{
				Client: fakeClient,
			}

			err := hook.ValidateCoreCRDs(ctx)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("policydefinitions.core.oam.dev"))
			Expect(err.Error()).Should(ContainSubstring("no OpenAPI schema found"))
		})
	})

	// Test Scenario 6: Multiple CRDs with various issues
	Context("when multiple CRDs have different issues", func() {
		It("should report all errors in a comprehensive error message", func() {
			// Create a mix of invalid CRDs
			invalidCRDs := []client.Object{
				createCRDWithWrongVersion("applications.core.oam.dev", "v1alpha2"),
				createCRDWithMissingField("traitdefinitions.core.oam.dev", "spec.schematic"),
				// PolicyDefinition is missing entirely
				createCRDWithoutSchema("workflowstepdefinitions.core.oam.dev"),
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(invalidCRDs...).
				Build()

			hook = &crdvalidation.Hook{
				Client: fakeClient,
			}

			err := hook.ValidateCoreCRDs(ctx)
			Expect(err).Should(HaveOccurred())

			// Check that multiple errors are reported
			Expect(err.Error()).Should(ContainSubstring("core CRD validation failed"))
			Expect(err.Error()).Should(ContainSubstring("applications.core.oam.dev"))
			Expect(err.Error()).Should(ContainSubstring("traitdefinitions.core.oam.dev"))
			Expect(err.Error()).Should(ContainSubstring("policydefinitions.core.oam.dev")) // missing
			Expect(err.Error()).Should(ContainSubstring("workflowstepdefinitions.core.oam.dev"))
		})
	})

	// Test Scenario 7: CRD validation passes with schema checks
	// Note: Round-trip tests are only performed for namespaced CRDs
	Context("when validating CRDs with schema checks", func() {
		It("should validate namespaced CRDs successfully with round-trip test", func() {
			validCRDs := createValidCoreCRDs()

			// Create test Application resource to support round-trip test
			testApp := &v1beta1.Application{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-app",
					Namespace: "default",
				},
				Spec: v1beta1.ApplicationSpec{
					Components: []common.ApplicationComponent{
						{
							Name: "test-comp",
							Type: "webservice",
						},
					},
				},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(validCRDs...).
				WithObjects(testApp).
				Build()

			hook = &crdvalidation.Hook{
				Client: fakeClient,
			}

			err := hook.ValidateCoreCRDs(ctx)
			Expect(err).Should(Succeed())
		})

		It("should validate cluster-scoped CRDs successfully with schema validation only", func() {
			// Cluster-scoped CRDs (TraitDefinition, PolicyDefinition, WorkflowStepDefinition)
			// only get schema validation, not round-trip tests, to avoid namespace-related issues
			validCRDs := createValidCoreCRDs()

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(validCRDs...).
				Build()

			hook = &crdvalidation.Hook{
				Client: fakeClient,
			}

			err := hook.ValidateCoreCRDs(ctx)
			Expect(err).Should(Succeed())
		})
	})

	// Test Scenario 8: Edge cases in field path checking
	Context("when checking field paths with edge cases", func() {
		It("should handle deeply nested field paths correctly", func() {
			// Create CRD with deeply nested structure
			deeplyNestedCRD := createCRDWithDeepNesting("applications.core.oam.dev")

			// Add a test Application to support round-trip test
			testApp := &v1beta1.Application{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-nested-app",
					Namespace: "default",
				},
				Spec: v1beta1.ApplicationSpec{
					Components: []common.ApplicationComponent{
						{Name: "comp1", Type: "webservice"},
					},
				},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(deeplyNestedCRD).
				WithObjects(createValidCoreCRDs()[1:]...).
				WithObjects(testApp).
				Build()

			hook = &crdvalidation.Hook{
				Client: fakeClient,
			}

			err := hook.ValidateCoreCRDs(ctx)
			Expect(err).Should(Succeed())
		})

		It("should fail when intermediate field in path has no properties", func() {
			// Create CRD where spec exists but has no properties defined
			// This tests the case where a field exists in the schema but cannot be navigated into
			// because it has no nested properties, which prevents checking required nested fields
			invalidNestedCRD := createCRDWithInvalidNesting("applications.core.oam.dev")

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(invalidNestedCRD).
				WithObjects(createValidCoreCRDs()[1:]...).
				Build()

			hook = &crdvalidation.Hook{
				Client: fakeClient,
			}

			err := hook.ValidateCoreCRDs(ctx)
			Expect(err).Should(HaveOccurred())
			// When spec has no properties, required nested fields like spec.components cannot be found
			Expect(err.Error()).Should(ContainSubstring("schema missing required fields"))
			Expect(err.Error()).Should(ContainSubstring("spec.components"))
		})
	})

	// Test Scenario 9: Validation with all CRDs having various storage versions
	Context("when CRDs have different storage versions", func() {
		It("should pass as long as v1beta1 is served", func() {
			// Create CRDs where v1beta1 is served but not storage
			multiVersionCRDs := createCRDsWithMultipleVersions()

			// Add test Application to support round-trip test for namespaced CRD
			testApp := &v1beta1.Application{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-multiversion-app",
					Namespace: "default",
				},
				Spec: v1beta1.ApplicationSpec{
					Components: []common.ApplicationComponent{
						{Name: "comp1", Type: "webservice"},
					},
				},
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(multiVersionCRDs...).
				WithObjects(testApp).
				Build()

			hook = &crdvalidation.Hook{
				Client: fakeClient,
			}

			err := hook.ValidateCoreCRDs(ctx)
			Expect(err).Should(Succeed())
		})
	})

	// Test Scenario 10: Partial schema validation
	Context("when CRD has partial schema (some fields missing)", func() {
		It("should report specific missing fields", func() {
			// Create Application CRD missing multiple required fields
			partialSchemaCRD := createCRDWithPartialSchema("applications.core.oam.dev")
			otherCRDs := createValidCoreCRDs()[1:]

			allCRDs := append([]client.Object{partialSchemaCRD}, otherCRDs...)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(allCRDs...).
				Build()

			hook = &crdvalidation.Hook{
				Client: fakeClient,
			}

			err := hook.ValidateCoreCRDs(ctx)
			Expect(err).Should(HaveOccurred())
			// Should mention missing fields
			Expect(err.Error()).Should(ContainSubstring("workflow"))
			Expect(err.Error()).Should(ContainSubstring("policies"))
		})
	})
})

// Helper functions to create various CRD configurations for testing

func createValidCoreCRDs() []client.Object {
	preserveUnknownFields := false

	return []client.Object{
		// Application CRD
		&apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: "applications.core.oam.dev",
			},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "core.oam.dev",
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{
						Name:    "v1beta1",
						Served:  true,
						Storage: true,
						Schema: &apiextensionsv1.CustomResourceValidation{
							OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
								Type: "object",
								Properties: map[string]apiextensionsv1.JSONSchemaProps{
									"spec": {
										Type: "object",
										Properties: map[string]apiextensionsv1.JSONSchemaProps{
											"components": {
												Type:                   "array",
												XPreserveUnknownFields: &preserveUnknownFields,
											},
											"workflow": {
												Type: "object",
											},
											"policies": {
												Type: "array",
											},
										},
									},
									"status": {
										Type: "object",
									},
								},
							},
						},
					},
				},
				Scope: apiextensionsv1.NamespaceScoped,
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   "applications",
					Singular: "application",
					Kind:     "Application",
				},
			},
		},
		// TraitDefinition CRD
		&apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: "traitdefinitions.core.oam.dev",
			},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "core.oam.dev",
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{
						Name:    "v1beta1",
						Served:  true,
						Storage: true,
						Schema: &apiextensionsv1.CustomResourceValidation{
							OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
								Type: "object",
								Properties: map[string]apiextensionsv1.JSONSchemaProps{
									"spec": {
										Type: "object",
										Properties: map[string]apiextensionsv1.JSONSchemaProps{
											"schematic": {
												Type: "object",
											},
											"appliesToWorkloads": {
												Type: "array",
											},
											"workloadRefPath": {
												Type: "string",
											},
										},
									},
									"status": {
										Type: "object",
									},
								},
							},
						},
					},
				},
				Scope: apiextensionsv1.ClusterScoped,
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   "traitdefinitions",
					Singular: "traitdefinition",
					Kind:     "TraitDefinition",
				},
			},
		},
		// PolicyDefinition CRD
		&apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: "policydefinitions.core.oam.dev",
			},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "core.oam.dev",
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{
						Name:    "v1beta1",
						Served:  true,
						Storage: true,
						Schema: &apiextensionsv1.CustomResourceValidation{
							OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
								Type: "object",
								Properties: map[string]apiextensionsv1.JSONSchemaProps{
									"spec": {
										Type: "object",
										Properties: map[string]apiextensionsv1.JSONSchemaProps{
											"schematic": {
												Type: "object",
											},
											"definitionRef": {
												Type: "object",
											},
										},
									},
									"status": {
										Type: "object",
									},
								},
							},
						},
					},
				},
				Scope: apiextensionsv1.ClusterScoped,
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   "policydefinitions",
					Singular: "policydefinition",
					Kind:     "PolicyDefinition",
				},
			},
		},
		// WorkflowStepDefinition CRD
		&apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: "workflowstepdefinitions.core.oam.dev",
			},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "core.oam.dev",
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{
						Name:    "v1beta1",
						Served:  true,
						Storage: true,
						Schema: &apiextensionsv1.CustomResourceValidation{
							OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
								Type: "object",
								Properties: map[string]apiextensionsv1.JSONSchemaProps{
									"spec": {
										Type: "object",
										Properties: map[string]apiextensionsv1.JSONSchemaProps{
											"schematic": {
												Type: "object",
											},
											"definitionRef": {
												Type: "object",
											},
										},
									},
									"status": {
										Type: "object",
									},
								},
							},
						},
					},
				},
				Scope: apiextensionsv1.ClusterScoped,
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   "workflowstepdefinitions",
					Singular: "workflowstepdefinition",
					Kind:     "WorkflowStepDefinition",
				},
			},
		},
	}
}

func createCRDWithWrongVersion(name, version string) client.Object {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "core.oam.dev",
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    version,
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
						},
					},
				},
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "tests",
				Singular: "test",
				Kind:     "Test",
			},
		},
	}
}

func createCRDWithUnservedVersion(name string) client.Object {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "core.oam.dev",
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1beta1",
					Served:  false, // Not served
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
						},
					},
				},
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "tests",
				Singular: "test",
				Kind:     "Test",
			},
		},
	}
}

func createCRDWithMissingField(name, missingField string) client.Object {
	// Create base properties without the missing field
	properties := map[string]apiextensionsv1.JSONSchemaProps{
		"spec": {
			Type:       "object",
			Properties: map[string]apiextensionsv1.JSONSchemaProps{
				// Add fields based on CRD type but skip the missing field
			},
		},
		"status": {
			Type: "object",
		},
	}

	// Add specific fields based on CRD type (except the missing one)
	// This ensures we test detection of the specific missing field for each CRD type
	if name == "applications.core.oam.dev" && missingField != "spec.components" {
		properties["spec"].Properties["components"] = apiextensionsv1.JSONSchemaProps{Type: "array"}
	}
	if name == "traitdefinitions.core.oam.dev" && missingField != "spec.schematic" {
		properties["spec"].Properties["schematic"] = apiextensionsv1.JSONSchemaProps{Type: "object"}
	}
	if name == "traitdefinitions.core.oam.dev" && missingField != "spec.appliesToWorkloads" {
		properties["spec"].Properties["appliesToWorkloads"] = apiextensionsv1.JSONSchemaProps{Type: "array"}
	}
	if name == "traitdefinitions.core.oam.dev" && missingField != "spec.workloadRefPath" {
		properties["spec"].Properties["workloadRefPath"] = apiextensionsv1.JSONSchemaProps{Type: "string"}
	}
	if name == "policydefinitions.core.oam.dev" && missingField != "spec.schematic" {
		properties["spec"].Properties["schematic"] = apiextensionsv1.JSONSchemaProps{Type: "object"}
	}
	if name == "policydefinitions.core.oam.dev" && missingField != "spec.definitionRef" {
		properties["spec"].Properties["definitionRef"] = apiextensionsv1.JSONSchemaProps{Type: "object"}
	}
	if name == "workflowstepdefinitions.core.oam.dev" && missingField != "spec.schematic" {
		properties["spec"].Properties["schematic"] = apiextensionsv1.JSONSchemaProps{Type: "object"}
	}
	if name == "workflowstepdefinitions.core.oam.dev" && missingField != "spec.definitionRef" {
		properties["spec"].Properties["definitionRef"] = apiextensionsv1.JSONSchemaProps{Type: "object"}
	}

	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "core.oam.dev",
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1beta1",
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type:       "object",
							Properties: properties,
						},
					},
				},
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "tests",
				Singular: "test",
				Kind:     "Test",
			},
		},
	}
}

func createCRDWithoutSpec(name string) client.Object {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "core.oam.dev",
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1beta1",
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
							Properties: map[string]apiextensionsv1.JSONSchemaProps{
								"status": {
									Type: "object",
								},
								// No spec field
							},
						},
					},
				},
			},
			Scope: apiextensionsv1.ClusterScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "tests",
				Singular: "test",
				Kind:     "Test",
			},
		},
	}
}

func createCRDWithoutSchema(name string) client.Object {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "core.oam.dev",
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1beta1",
					Served:  true,
					Storage: true,
					// No Schema field
				},
			},
			Scope: apiextensionsv1.ClusterScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "tests",
				Singular: "test",
				Kind:     "Test",
			},
		},
	}
}

func createCRDWithDeepNesting(name string) client.Object {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "core.oam.dev",
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1beta1",
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
							Properties: map[string]apiextensionsv1.JSONSchemaProps{
								"spec": {
									Type: "object",
									Properties: map[string]apiextensionsv1.JSONSchemaProps{
										"components": {
											Type: "array",
											Items: &apiextensionsv1.JSONSchemaPropsOrArray{
												Schema: &apiextensionsv1.JSONSchemaProps{
													Type: "object",
													Properties: map[string]apiextensionsv1.JSONSchemaProps{
														"traits": {
															Type: "array",
														},
													},
												},
											},
										},
										"workflow": {
											Type: "object",
											Properties: map[string]apiextensionsv1.JSONSchemaProps{
												"steps": {
													Type: "array",
												},
											},
										},
										"policies": {
											Type: "array",
										},
									},
								},
								"status": {
									Type: "object",
								},
							},
						},
					},
				},
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "applications",
				Singular: "application",
				Kind:     "Application",
			},
		},
	}
}

func createCRDWithInvalidNesting(name string) client.Object {
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "core.oam.dev",
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1beta1",
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
							Properties: map[string]apiextensionsv1.JSONSchemaProps{
								"spec": {
									Type: "object",
									// Properties exists but is nil/empty - this should trigger the "has no properties" error
									// when trying to access nested fields like spec.components
								},
								"status": {
									Type: "object",
								},
							},
						},
					},
				},
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "applications",
				Singular: "application",
				Kind:     "Application",
			},
		},
	}
}

func createCRDsWithMultipleVersions() []client.Object {
	preserveUnknownFields := false

	return []client.Object{
		// Application with multiple versions
		&apiextensionsv1.CustomResourceDefinition{
			ObjectMeta: metav1.ObjectMeta{
				Name: "applications.core.oam.dev",
			},
			Spec: apiextensionsv1.CustomResourceDefinitionSpec{
				Group: "core.oam.dev",
				Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
					{
						Name:    "v1alpha2",
						Served:  true,
						Storage: true, // v1alpha2 is storage
						Schema: &apiextensionsv1.CustomResourceValidation{
							OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
								Type: "object",
								Properties: map[string]apiextensionsv1.JSONSchemaProps{
									"spec": {
										Type: "object",
										Properties: map[string]apiextensionsv1.JSONSchemaProps{
											"components": {
												Type:                   "array",
												XPreserveUnknownFields: &preserveUnknownFields,
											},
											"workflow": {
												Type: "object",
											},
											"policies": {
												Type: "array",
											},
										},
									},
									"status": {
										Type: "object",
									},
								},
							},
						},
					},
					{
						Name:    "v1beta1",
						Served:  true,
						Storage: false, // v1beta1 is served but not storage
						Schema: &apiextensionsv1.CustomResourceValidation{
							OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
								Type: "object",
								Properties: map[string]apiextensionsv1.JSONSchemaProps{
									"spec": {
										Type: "object",
										Properties: map[string]apiextensionsv1.JSONSchemaProps{
											"components": {
												Type:                   "array",
												XPreserveUnknownFields: &preserveUnknownFields,
											},
											"workflow": {
												Type: "object",
											},
											"policies": {
												Type: "array",
											},
										},
									},
									"status": {
										Type: "object",
									},
								},
							},
						},
					},
				},
				Scope: apiextensionsv1.NamespaceScoped,
				Names: apiextensionsv1.CustomResourceDefinitionNames{
					Plural:   "applications",
					Singular: "application",
					Kind:     "Application",
				},
			},
		},
		// Add other CRDs with standard v1beta1 versions
		createValidCoreCRDs()[1],
		createValidCoreCRDs()[2],
		createValidCoreCRDs()[3],
	}
}

func createCRDWithPartialSchema(name string) client.Object {
	// Create Application CRD with only spec.components, missing workflow and policies
	return &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "core.oam.dev",
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    "v1beta1",
					Served:  true,
					Storage: true,
					Schema: &apiextensionsv1.CustomResourceValidation{
						OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
							Type: "object",
							Properties: map[string]apiextensionsv1.JSONSchemaProps{
								"spec": {
									Type: "object",
									Properties: map[string]apiextensionsv1.JSONSchemaProps{
										"components": {
											Type: "array",
										},
										// Missing workflow and policies fields
									},
								},
								"status": {
									Type: "object",
								},
							},
						},
					},
				},
			},
			Scope: apiextensionsv1.NamespaceScoped,
			Names: apiextensionsv1.CustomResourceDefinitionNames{
				Plural:   "applications",
				Singular: "application",
				Kind:     "Application",
			},
		},
	}
}
