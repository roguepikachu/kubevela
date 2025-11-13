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

package schema_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/cmd/core/app/hooks/schema"
)

func TestCRDSchemaValidation(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "CRD Schema Validation Suite")
}

var _ = Describe("CRD schema validation hook", func() {
	var scheme *runtime.Scheme

	BeforeEach(func() {
		// Create scheme with apiextensions types registered
		scheme = runtime.NewScheme()
		Expect(apiextensionsv1.AddToScheme(scheme)).Should(Succeed())
	})

	Context("with valid CRDs", func() {
		It("should succeed when all required CRDs exist with proper schemas", func() {
			// This test requires actual CRDs to be loaded
			// Skip if not in integration test environment
			Skip("Requires actual CRD definitions - run with integration tests")
		})
	})

	Context("with missing CRDs", func() {
		It("should report missing critical CRDs as errors", func() {
			ctx := context.Background()

			// Create a fake client with no CRDs
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			hook := &schema.SchemaValidator{
				Client: fakeClient,
			}

			err := hook.Run(ctx)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("missing critical CRDs"))
		})

		It("should report missing non-critical CRDs as warnings only", func() {
			ctx := context.Background()

			// Create a fake client with only critical CRDs
			criticalCRDs := []client.Object{
				createTestCRD("applications.core.oam.dev"),
				createTestCRD("applicationrevisions.core.oam.dev"),
				createTestCRD("resourcetrackers.core.oam.dev"),
				createTestCRD("traitdefinitions.core.oam.dev"),
				createTestCRD("policydefinitions.core.oam.dev"),
				createTestCRD("workflowstepdefinitions.core.oam.dev"),
				createTestCRD("componentdefinitions.core.oam.dev"),
				createTestCRD("definitionrevisions.core.oam.dev"),
				createTestCRD("workloaddefinitions.core.oam.dev"),
			}

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(criticalCRDs...).
				Build()

			hook := &schema.SchemaValidator{
				Client: fakeClient,
			}

			// Should succeed even though non-critical CRDs (workflows, policies) are missing
			err := hook.Run(ctx)
			Expect(err).Should(Succeed())
		})
	})

	Context("with invalid CRD schemas", func() {
		It("should fail if CRD missing OpenAPI schema", func() {
			ctx := context.Background()

			// Create CRD without OpenAPI schema
			invalidCRD := &apiextensionsv1.CustomResourceDefinition{
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
							// Missing Schema field
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

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(invalidCRD).
				Build()

			hook := &schema.SchemaValidator{
				Client: fakeClient,
			}

			err := hook.Run(ctx)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("missing OpenAPI v3 schema"))
		})

		It("should fail if CRD has no served version", func() {
			ctx := context.Background()

			// Create CRD with no served versions
			crd := createTestCRD("applications.core.oam.dev")
			crdDef := crd.(*apiextensionsv1.CustomResourceDefinition)
			crdDef.Spec.Versions[0].Served = false

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(crd).
				Build()

			hook := &schema.SchemaValidator{
				Client: fakeClient,
			}

			err := hook.Run(ctx)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("no served version"))
		})
	})
})

// createTestCRD creates a minimal valid CRD for testing
func createTestCRD(name string) client.Object {
	preserveUnknownFields := false
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
									Type:                   "object",
									XPreserveUnknownFields: &preserveUnknownFields,
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
				Plural:   "tests",
				Singular: "test",
				Kind:     "Test",
			},
		},
	}
}
