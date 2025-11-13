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

package featuregate_test

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	featuregatetesting "k8s.io/component-base/featuregate/testing"

	"github.com/oam-dev/kubevela/cmd/core/app/hooks/featuregate"
	"github.com/oam-dev/kubevela/pkg/features"
)

func TestFeatureGateDependencyValidation(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Feature Gate Dependency Validation Suite")
}

var _ = Describe("Feature gate dependency validation hook", func() {
	var hook *featuregate.DependencyValidator
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
		hook = featuregate.NewHook().(*featuregate.DependencyValidator)
		Expect(hook).ShouldNot(BeNil())
		Expect(hook.Name()).Should(Equal("FeatureGateDependencyValidation"))
	})

	Context("when SharedDefinitionStorageForApplicationRevision is enabled", func() {
		It("should fail if InformerCacheFilterUnnecessaryFields is not enabled", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate,
				features.SharedDefinitionStorageForApplicationRevision, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate,
				features.InformerCacheFilterUnnecessaryFields, false)

			err := hook.Run(ctx)
			Expect(err).Should(HaveOccurred())
			Expect(err.Error()).Should(ContainSubstring("SharedDefinitionStorageForApplicationRevision"))
			Expect(err.Error()).Should(ContainSubstring("requires"))
			Expect(err.Error()).Should(ContainSubstring("InformerCacheFilterUnnecessaryFields"))
		})

		It("should succeed if InformerCacheFilterUnnecessaryFields is enabled", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate,
				features.SharedDefinitionStorageForApplicationRevision, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate,
				features.InformerCacheFilterUnnecessaryFields, true)

			err := hook.Run(ctx)
			Expect(err).Should(Succeed())
		})
	})

	Context("when ApplyOnce is enabled", func() {
		It("should warn if compression features are enabled (but not fail)", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate,
				features.ApplyOnce, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate,
				features.GzipResourceTracker, true)

			// Should succeed since it's only a warning
			err := hook.Run(ctx)
			Expect(err).Should(Succeed())
		})
	})

	Context("when multiple compression types are enabled", func() {
		It("should log priority information without failing", func() {
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate,
				features.ZstdApplicationRevision, true)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate,
				features.GzipApplicationRevision, true)

			err := hook.Run(ctx)
			Expect(err).Should(Succeed())
		})
	})

	Context("when no conflicting features are enabled", func() {
		It("should succeed", func() {
			// Disable all potentially conflicting features
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate,
				features.SharedDefinitionStorageForApplicationRevision, false)
			featuregatetesting.SetFeatureGateDuringTest(GinkgoT(), utilfeature.DefaultFeatureGate,
				features.ApplyOnce, false)

			err := hook.Run(ctx)
			Expect(err).Should(Succeed())
		})
	})
})
