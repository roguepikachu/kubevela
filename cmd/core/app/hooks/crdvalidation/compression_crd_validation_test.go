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
	"fmt"

	"github.com/kubevela/pkg/util/compression"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/oam-dev/kubevela/apis/core.oam.dev/common"
	"github.com/oam-dev/kubevela/apis/core.oam.dev/v1beta1"
	"github.com/oam-dev/kubevela/apis/types"
	"github.com/oam-dev/kubevela/cmd/core/app/hooks/crdvalidation"
	"github.com/oam-dev/kubevela/pkg/features"
	"github.com/oam-dev/kubevela/pkg/oam"
)

// Test suite is defined in crd_validation_test.go
// All tests in this file run under the main CRD Validation Hook Suite

var _ = Describe("Compression CRD Validation", func() {
	var (
		ctx    context.Context
		scheme *runtime.Scheme
		hook   *crdvalidation.Hook
	)

	BeforeEach(func() {
		ctx = context.Background()

		// Create scheme with all required types
		scheme = runtime.NewScheme()
		Expect(v1beta1.AddToScheme(scheme)).Should(Succeed())
		Expect(corev1.AddToScheme(scheme)).Should(Succeed())
	})

	// Test Scenario 1: No compression features enabled
	Context("when no compression features are enabled", func() {
		It("should skip validation and succeed", func() {
			// Disable all compression features
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdApplicationRevision, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipApplicationRevision, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdResourceTracker, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipResourceTracker, false)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			hook = &crdvalidation.Hook{
				Client: fakeClient,
			}

			err := hook.ValidateCompressionCRDs(ctx)
			Expect(err).Should(Succeed(), "Should skip validation when no compression features are enabled")
		})
	})

	// Test Scenario 2: ApplicationRevision with Zstd compression enabled
	Context("when ApplicationRevision Zstd compression is enabled", func() {
		It("should successfully validate with proper CRD support", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdApplicationRevision, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipApplicationRevision, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdResourceTracker, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipResourceTracker, false)

			// Create a test ApplicationRevision that will be returned during validation
			testAppRev := createTestApplicationRevision("test-apprev", compression.Zstd)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(testAppRev).
				Build()

			// Wrap with interceptor to simulate successful round-trip
			mockClient := &roundTripSimulatorClient{
				Client:              fakeClient,
				simulateCompression: true,
			}

			hook = &crdvalidation.Hook{
				Client: mockClient,
			}

			err := hook.ValidateCompressionCRDs(ctx)
			Expect(err).Should(Succeed(), "Should validate successfully with Zstd compression")
		})

		It("should fail when CRD doesn't support compression fields", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdApplicationRevision, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipApplicationRevision, false)

			// Use client that simulates data corruption on round-trip
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			mockClient := &roundTripSimulatorClient{
				Client:              fakeClient,
				simulateCompression: false, // Simulate CRD not supporting compression
			}

			hook = &crdvalidation.Hook{
				Client: mockClient,
			}

			err := hook.ValidateCompressionCRDs(ctx)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("ApplicationRevision CRD is not updated"))
			Expect(err.Error()).Should(ContainSubstring("Compression cannot be used"))
		})
	})

	// Test Scenario 3: ApplicationRevision with Gzip compression enabled
	Context("when ApplicationRevision Gzip compression is enabled", func() {
		It("should successfully validate with proper CRD support", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdApplicationRevision, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipApplicationRevision, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdResourceTracker, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipResourceTracker, false)

			// Create a test ApplicationRevision that will be returned during validation
			testAppRev := createTestApplicationRevision("test-apprev", compression.Gzip)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(testAppRev).
				Build()

			// Wrap with interceptor to simulate successful round-trip
			mockClient := &roundTripSimulatorClient{
				Client:              fakeClient,
				simulateCompression: true,
			}

			hook = &crdvalidation.Hook{
				Client: mockClient,
			}

			err := hook.ValidateCompressionCRDs(ctx)
			Expect(err).Should(Succeed(), "Should validate successfully with Gzip compression")
		})
	})

	// Test Scenario 4: Both ApplicationRevision compression types enabled (priority test)
	Context("when both ApplicationRevision Zstd and Gzip are enabled", func() {
		It("should use Zstd compression (higher priority) and succeed", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdApplicationRevision, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipApplicationRevision, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdResourceTracker, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipResourceTracker, false)

			// Track which compression type was used
			var usedCompression compression.Type
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			mockClient := &compressionTypeCapturingClient{
				Client:              fakeClient,
				captureCompression:  &usedCompression,
				simulateCompression: true,
			}

			hook = &crdvalidation.Hook{
				Client: mockClient,
			}

			err := hook.ValidateCompressionCRDs(ctx)
			Expect(err).Should(Succeed())
			Expect(usedCompression).Should(Equal(compression.Zstd), "Zstd should take priority over Gzip")
		})
	})

	// Test Scenario 5: ResourceTracker with Zstd compression enabled
	Context("when ResourceTracker Zstd compression is enabled", func() {
		It("should successfully validate with proper CRD support", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdApplicationRevision, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipApplicationRevision, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdResourceTracker, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipResourceTracker, false)

			// Create a test ResourceTracker
			testRT := createTestResourceTracker("test-rt", compression.Zstd)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(testRT).
				Build()

			// Wrap with interceptor to simulate successful round-trip
			mockClient := &roundTripSimulatorClient{
				Client:              fakeClient,
				simulateCompression: true,
			}

			hook = &crdvalidation.Hook{
				Client: mockClient,
			}

			err := hook.ValidateCompressionCRDs(ctx)
			Expect(err).Should(Succeed(), "Should validate successfully with Zstd compression")
		})

		It("should fail when CRD doesn't support compression fields", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdResourceTracker, true)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			// Simulate ResourceTracker CRD not supporting compression
			mockClient := &resourceTrackerCorruptingClient{
				Client: fakeClient,
			}

			hook = &crdvalidation.Hook{
				Client: mockClient,
			}

			err := hook.ValidateCompressionCRDs(ctx)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("ResourceTracker CRD is not updated"))
		})
	})

	// Test Scenario 6: ResourceTracker with Gzip compression enabled
	Context("when ResourceTracker Gzip compression is enabled", func() {
		It("should successfully validate with proper CRD support", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdApplicationRevision, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipApplicationRevision, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdResourceTracker, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipResourceTracker, true)

			// Create a test ResourceTracker
			testRT := createTestResourceTracker("test-rt", compression.Gzip)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(testRT).
				Build()

			// Wrap with interceptor to simulate successful round-trip
			mockClient := &roundTripSimulatorClient{
				Client:              fakeClient,
				simulateCompression: true,
			}

			hook = &crdvalidation.Hook{
				Client: mockClient,
			}

			err := hook.ValidateCompressionCRDs(ctx)
			Expect(err).Should(Succeed(), "Should validate successfully with Gzip compression")
		})
	})

	// Test Scenario 7: Both ResourceTracker compression types enabled (priority test)
	Context("when both ResourceTracker Zstd and Gzip are enabled", func() {
		It("should use Zstd compression (higher priority) and succeed", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdApplicationRevision, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipApplicationRevision, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdResourceTracker, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipResourceTracker, true)

			// Track which compression type was used
			var usedCompression compression.Type
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			mockClient := &compressionTypeCapturingClient{
				Client:              fakeClient,
				captureCompression:  &usedCompression,
				simulateCompression: true,
			}

			hook = &crdvalidation.Hook{
				Client: mockClient,
			}

			err := hook.ValidateCompressionCRDs(ctx)
			Expect(err).Should(Succeed())
			Expect(usedCompression).Should(Equal(compression.Zstd), "Zstd should take priority over Gzip")
		})
	})

	// Test Scenario 8: All compression features enabled
	Context("when all compression features are enabled", func() {
		It("should validate both ApplicationRevision and ResourceTracker with Zstd priority", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdApplicationRevision, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipApplicationRevision, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdResourceTracker, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipResourceTracker, true)

			// Track created resources
			var createdResources []string
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			mockClient := &resourceCreationTrackingClient{
				Client:           fakeClient,
				createdResources: &createdResources,
			}

			hook = &crdvalidation.Hook{
				Client: mockClient,
			}

			err := hook.ValidateCompressionCRDs(ctx)
			Expect(err).Should(Succeed())
			// Should have created both ApplicationRevision and ResourceTracker test resources
			Expect(createdResources).Should(HaveLen(2))
			Expect(createdResources).Should(ContainElements(
				ContainSubstring("ApplicationRevision"),
				ContainSubstring("ResourceTracker"),
			))
		})
	})

	// Test Scenario 9: Mixed success and failure
	Context("when ApplicationRevision succeeds but ResourceTracker fails", func() {
		It("should report ResourceTracker failure", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdApplicationRevision, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipApplicationRevision, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdResourceTracker, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipResourceTracker, false)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			// Simulate ApplicationRevision success but ResourceTracker failure
			mockClient := &selectiveFailureClient{
				Client:                  fakeClient,
				failResourceTracker:     true,
				failApplicationRevision: false,
			}

			hook = &crdvalidation.Hook{
				Client: mockClient,
			}

			err := hook.ValidateCompressionCRDs(ctx)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("ResourceTracker"))
			Expect(err.Error()).ShouldNot(ContainSubstring("ApplicationRevision"))
		})
	})

	// Test Scenario 10: Both ApplicationRevision and ResourceTracker fail
	Context("when both ApplicationRevision and ResourceTracker fail", func() {
		It("should report both failures", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdApplicationRevision, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdResourceTracker, true)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			// Simulate both failures
			mockClient := &selectiveFailureClient{
				Client:                  fakeClient,
				failResourceTracker:     true,
				failApplicationRevision: true,
			}

			hook = &crdvalidation.Hook{
				Client: mockClient,
			}

			err := hook.ValidateCompressionCRDs(ctx)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("ApplicationRevision"))
			Expect(err.Error()).Should(ContainSubstring("ResourceTracker"))
			Expect(err.Error()).Should(ContainSubstring("compression CRD validation failed"))
		})
	})

	// Test Scenario 11: ResourceTracker with empty ManagedResources
	Context("when ResourceTracker round-trip loses ManagedResources", func() {
		It("should fail validation", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.ZstdResourceTracker, true)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			// Simulate ResourceTracker losing ManagedResources
			mockClient := &managedResourcesLossClient{
				Client: fakeClient,
			}

			hook = &crdvalidation.Hook{
				Client: mockClient,
			}

			err := hook.ValidateCompressionCRDs(ctx)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("ResourceTracker CRD is not updated"))
		})
	})

	// Test Scenario 12: ResourceTracker with corrupted ManagedResource data
	Context("when ResourceTracker round-trip corrupts ManagedResource data", func() {
		It("should fail validation", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate, features.GzipResourceTracker, true)

			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				Build()

			// Simulate ResourceTracker corrupting ManagedResource data
			mockClient := &managedResourceCorruptionClient{
				Client: fakeClient,
			}

			hook = &crdvalidation.Hook{
				Client: mockClient,
			}

			err := hook.ValidateCompressionCRDs(ctx)
			Expect(err).Should(HaveOccurred())
			// The error can be about either lost resources or corrupted data
			Expect(err.Error()).Should(Or(
				ContainSubstring("ResourceTracker CRD is not updated"),
				ContainSubstring("compression validation failed"),
			))
		})
	})
})

// Helper functions and mock clients

func createTestApplicationRevision(name string, compressionType compression.Type) *v1beta1.ApplicationRevision {
	appRev := &v1beta1.ApplicationRevision{}
	appRev.Name = name
	appRev.Namespace = "vela-system"
	appRev.Labels = map[string]string{oam.LabelPreCheck: types.VelaCoreName}
	appRev.Spec.Application.Name = name
	appRev.Spec.Application.Spec.Components = []common.ApplicationComponent{}
	appRev.Spec.Compression.SetType(compressionType)
	return appRev
}

func createTestResourceTracker(name string, compressionType compression.Type) *v1beta1.ResourceTracker {
	rt := &v1beta1.ResourceTracker{}
	rt.Name = name
	rt.Labels = map[string]string{
		oam.LabelPreCheck:    types.VelaCoreName,
		oam.LabelAppName:     name,
		oam.LabelAppRevision: name + "-v1",
	}
	rt.Spec.ManagedResources = []v1beta1.ManagedResource{
		{
			ClusterObjectReference: common.ClusterObjectReference{
				Cluster: "local",
				ObjectReference: corev1.ObjectReference{
					APIVersion: "v1",
					Kind:       "ConfigMap",
					Namespace:  "vela-system",
					Name:       name,
				},
			},
			OAMObjectReference: common.OAMObjectReference{
				Component: name + "-comp",
			},
			Data: &runtime.RawExtension{
				Raw: []byte(fmt.Sprintf(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"%s","namespace":"vela-system"},"data":{"test":"data"}}`, name)),
			},
			Deleted: false,
		},
	}
	rt.Spec.Compression.SetType(compressionType)
	return rt
}

// Mock client that simulates successful round-trip with compression support
// This mock wraps a real fake client and only overrides specific methods
type roundTripSimulatorClient struct {
	client.Client       // Embed the full client interface
	simulateCompression bool
}

func (c *roundTripSimulatorClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	// Store the object as-is
	return c.Client.Create(ctx, obj, opts...)
}

func (c *roundTripSimulatorClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	err := c.Client.Get(ctx, key, obj, opts...)
	if err != nil {
		return err
	}

	// If simulating compression support, return data as-is
	if c.simulateCompression {
		return nil
	}

	// Otherwise, corrupt the data to simulate CRD not supporting compression
	switch v := obj.(type) {
	case *v1beta1.ApplicationRevision:
		v.Spec.Application.Name = "corrupted"
	case *v1beta1.ResourceTracker:
		v.Spec.ManagedResources = []v1beta1.ManagedResource{}
	}
	return nil
}

// Mock client that captures the compression type used
type compressionTypeCapturingClient struct {
	client.Client
	captureCompression  *compression.Type
	simulateCompression bool
}

func (c *compressionTypeCapturingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	switch v := obj.(type) {
	case *v1beta1.ApplicationRevision:
		*c.captureCompression = v.Spec.Compression.Type
	case *v1beta1.ResourceTracker:
		*c.captureCompression = v.Spec.Compression.Type
	}
	return c.Client.Create(ctx, obj, opts...)
}

func (c *compressionTypeCapturingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	err := c.Client.Get(ctx, key, obj, opts...)
	if err != nil && c.simulateCompression {
		// Simulate successful round-trip by returning the object with correct data
		switch v := obj.(type) {
		case *v1beta1.ApplicationRevision:
			v.Name = key.Name
			v.Namespace = key.Namespace
			v.Spec.Application.Name = key.Name
			return nil
		case *v1beta1.ResourceTracker:
			v.Name = key.Name
			v.Spec.ManagedResources = []v1beta1.ManagedResource{
				{
					ClusterObjectReference: common.ClusterObjectReference{
						ObjectReference: corev1.ObjectReference{
							Name: key.Name,
						},
					},
				},
			}
			return nil
		}
	}
	return err
}

// Mock client that tracks created resources
type resourceCreationTrackingClient struct {
	client.Client
	createdResources *[]string
}

func (c *resourceCreationTrackingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	*c.createdResources = append(*c.createdResources, fmt.Sprintf("%T:%s", obj, obj.GetName()))
	return c.Client.Create(ctx, obj, opts...)
}

func (c *resourceCreationTrackingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	// Simulate successful round-trip
	switch v := obj.(type) {
	case *v1beta1.ApplicationRevision:
		v.Name = key.Name
		v.Namespace = key.Namespace
		v.Spec.Application.Name = key.Name
		return nil
	case *v1beta1.ResourceTracker:
		v.Name = key.Name
		v.Spec.ManagedResources = []v1beta1.ManagedResource{
			{
				ClusterObjectReference: common.ClusterObjectReference{
					ObjectReference: corev1.ObjectReference{
						Name: key.Name,
					},
				},
			},
		}
		return nil
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

// Mock client that selectively fails ApplicationRevision or ResourceTracker
type selectiveFailureClient struct {
	client.Client
	failApplicationRevision bool
	failResourceTracker     bool
}

func (c *selectiveFailureClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	switch v := obj.(type) {
	case *v1beta1.ApplicationRevision:
		if c.failApplicationRevision {
			v.Name = key.Name
			v.Namespace = key.Namespace
			v.Spec.Application.Name = "corrupted" // Corrupt data
			return nil
		}
		v.Name = key.Name
		v.Namespace = key.Namespace
		v.Spec.Application.Name = key.Name
		return nil
	case *v1beta1.ResourceTracker:
		if c.failResourceTracker {
			v.Name = key.Name
			v.Spec.ManagedResources = []v1beta1.ManagedResource{} // Empty resources
			return nil
		}
		v.Name = key.Name
		v.Spec.ManagedResources = []v1beta1.ManagedResource{
			{
				ClusterObjectReference: common.ClusterObjectReference{
					ObjectReference: corev1.ObjectReference{
						Name: key.Name,
					},
				},
			},
		}
		return nil
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

// Mock client that corrupts ResourceTracker
type resourceTrackerCorruptingClient struct {
	client.Client
}

func (c *resourceTrackerCorruptingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if rt, ok := obj.(*v1beta1.ResourceTracker); ok {
		rt.Name = key.Name
		rt.Spec.ManagedResources = []v1beta1.ManagedResource{} // Lose all managed resources
		return nil
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

// Mock client that loses ManagedResources
type managedResourcesLossClient struct {
	client.Client
}

func (c *managedResourcesLossClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if rt, ok := obj.(*v1beta1.ResourceTracker); ok {
		rt.Name = key.Name
		rt.Spec.ManagedResources = []v1beta1.ManagedResource{} // Empty array
		return nil
	}
	return c.Client.Get(ctx, key, obj, opts...)
}

// Mock client that corrupts ManagedResource data
type managedResourceCorruptionClient struct {
	client.Client
}

func (c *managedResourceCorruptionClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if rt, ok := obj.(*v1beta1.ResourceTracker); ok {
		rt.Name = key.Name
		rt.Spec.ManagedResources = []v1beta1.ManagedResource{
			{
				ClusterObjectReference: common.ClusterObjectReference{
					ObjectReference: corev1.ObjectReference{
						Name: "corrupted-name", // Wrong name
					},
				},
			},
		}
		return nil
	}
	return c.Client.Get(ctx, key, obj, opts...)
}
