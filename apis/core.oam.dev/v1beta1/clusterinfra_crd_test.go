/*
Copyright 2026 The KubeVela Authors.

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

package v1beta1

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	"github.com/stretchr/testify/require"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

const (
	clusterPlaneCRDPath     = "../../../charts/vela-core/crds/core.oam.dev_clusterplanes.yaml"
	clusterBlueprintCRDPath = "../../../charts/vela-core/crds/core.oam.dev_clusterblueprints.yaml"
)

func loadCRD(t GinkgoTInterface, path string) *apiextv1.CustomResourceDefinition {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "generated CRD must be present in charts/vela-core/crds: %s", path)
	crd := &apiextv1.CustomResourceDefinition{}
	require.NoError(t, yaml.Unmarshal(raw, crd))
	return crd
}

// The infraProvisioning blueprint is resolved by name in the SpokeCluster's own
// namespace, so both CRDs must be namespaced and must require the one list the
// controller reads.
var _ = It("ClusterPlaneCRD IsNamespacedAndRequiresComponents", func() {
	t := GinkgoT()
	r := require.New(t)
	crd := loadCRD(t, clusterPlaneCRDPath)
	r.Equal(apiextv1.NamespaceScoped, crd.Spec.Scope)
	r.Equal("clusterplanes", crd.Spec.Names.Plural)
	spec := v1beta1Schema(t, crd).Properties["spec"]
	r.Contains(spec.Required, "components")
	r.NotNil(spec.Properties["components"].Items.Schema.Properties["properties"].XPreserveUnknownFields,
		"component properties must be preserved verbatim, they are ComponentDefinition parameters")
})

var _ = It("ClusterBlueprintCRD IsNamespacedAndRequiresPlanes", func() {
	t := GinkgoT()
	r := require.New(t)
	crd := loadCRD(t, clusterBlueprintCRDPath)
	r.Equal(apiextv1.NamespaceScoped, crd.Spec.Scope)
	r.Equal("clusterblueprints", crd.Spec.Names.Plural)
	spec := v1beta1Schema(t, crd).Properties["spec"]
	r.Contains(spec.Required, "planes")
	plane := spec.Properties["planes"].Items.Schema
	r.Contains(plane.Required, "ref")
	r.Contains(plane.Properties["ref"].Required, "name")
})
