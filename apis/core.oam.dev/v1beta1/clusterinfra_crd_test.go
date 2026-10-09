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

	// dns1123LabelPattern is what both name fields must match: the rendered
	// Application component is "<plane>-<component>" and must be a DNS-1123 label.
	dns1123LabelPattern = `^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	nameHalfMaxLength   = int64(31)
)

func loadCRD(t GinkgoTInterface, path string) *apiextv1.CustomResourceDefinition {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "generated CRD must be present in charts/vela-core/crds: %s", path)
	crd := &apiextv1.CustomResourceDefinition{}
	require.NoError(t, yaml.Unmarshal(raw, crd))
	return crd
}

// v1beta1Version returns the v1beta1 version entry of the CRD.
func v1beta1Version(t GinkgoTInterface, crd *apiextv1.CustomResourceDefinition) apiextv1.CustomResourceDefinitionVersion {
	t.Helper()
	for _, v := range crd.Spec.Versions {
		if v.Name == "v1beta1" {
			return v
		}
	}
	t.Fatalf("v1beta1 version not found in CRD")
	return apiextv1.CustomResourceDefinitionVersion{}
}

// assertInfraCRDShape checks what ClusterPlane and ClusterBlueprint share: both
// are namespaced, in the oam category, have no status subresource (POC shape),
// and require exactly one list whose items carry a DNS-1123 name half.
func assertInfraCRDShape(t GinkgoTInterface, crd *apiextv1.CustomResourceDefinition, plural, shortName, listField string) *apiextv1.JSONSchemaProps {
	t.Helper()
	r := require.New(t)
	r.Equal(apiextv1.NamespaceScoped, crd.Spec.Scope)
	r.Equal(plural, crd.Spec.Names.Plural)
	r.Equal([]string{shortName}, crd.Spec.Names.ShortNames)
	r.Contains(crd.Spec.Names.Categories, "oam")

	version := v1beta1Version(t, crd)
	r.NotNil(version.Subresources, "controller-gen emits an empty subresources block")
	r.Nil(version.Subresources.Status, "POC shape has no status subresource")

	spec := v1beta1Schema(t, crd).Properties["spec"]
	r.Contains(spec.Required, listField)
	list := spec.Properties[listField]
	r.NotNil(list.MinItems)
	r.Equal(int64(1), *list.MinItems)

	item := list.Items.Schema
	r.Contains(item.Required, "name")
	name := item.Properties["name"]
	r.NotNil(name.MaxLength)
	r.Equal(nameHalfMaxLength, *name.MaxLength)
	r.Equal(dns1123LabelPattern, name.Pattern)
	return item
}

// The infraProvisioning blueprint is resolved by name in the SpokeCluster's own
// namespace, so both CRDs must be namespaced and must require the one list the
// controller reads.
var _ = It("ClusterPlaneCRD IsNamespacedAndRequiresComponents", func() {
	t := GinkgoT()
	r := require.New(t)
	crd := loadCRD(t, clusterPlaneCRDPath)
	component := assertInfraCRDShape(t, crd, "clusterplanes", "cplane", "components")
	r.Contains(component.Required, "type")
	r.NotContains(component.Required, "dependsOn")
	r.NotContains(component.Required, "properties")
	r.NotNil(component.Properties["properties"].XPreserveUnknownFields,
		"component properties must be preserved verbatim, they are ComponentDefinition parameters")
})

var _ = It("ClusterBlueprintCRD IsNamespacedAndRequiresPlanes", func() {
	t := GinkgoT()
	r := require.New(t)
	crd := loadCRD(t, clusterBlueprintCRDPath)
	plane := assertInfraCRDShape(t, crd, "clusterblueprints", "cbp", "planes")
	r.Contains(plane.Required, "ref")
	r.NotContains(plane.Required, "dependsOn")
	r.Contains(plane.Properties["ref"].Required, "name")
})
