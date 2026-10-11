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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	"github.com/stretchr/testify/require"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

const (
	clusterPlaneCRDPath             = "../../../charts/vela-core/crds/core.oam.dev_clusterplanes.yaml"
	clusterBlueprintCRDPath         = "../../../charts/vela-core/crds/core.oam.dev_clusterblueprints.yaml"
	clusterPlaneRevisionCRDPath     = "../../../charts/vela-core/crds/core.oam.dev_clusterplanerevisions.yaml"
	clusterBlueprintRevisionCRDPath = "../../../charts/vela-core/crds/core.oam.dev_clusterblueprintrevisions.yaml"
	clusterCRDPath                  = "../../../charts/vela-core/crds/core.oam.dev_clusters.yaml"

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
	assertLifecyclePhaseEnum(t, plane.Properties["phase"])
	r.NotContains(plane.Required, "phase", "phase defaults, so an older blueprint without it still applies")
	r.NotNil(plane.Properties["phase"].Default)
	r.JSONEq(`"planeProvisioning"`, string(plane.Properties["phase"].Default.Raw))
})

// lifecyclePhaseEnum is the closed set a blueprint plane's phase may take, in
// the order the spoke runs them.
var lifecyclePhaseEnum = []string{"clusterInit", "planeProvisioning", "healthValidation"}

// assertLifecyclePhaseEnum checks that a phase property carries exactly the
// three lifecycle stages, so a typo in a blueprint is refused at apply time.
func assertLifecyclePhaseEnum(t GinkgoTInterface, phase apiextv1.JSONSchemaProps) {
	t.Helper()
	r := require.New(t)
	r.Equal("string", phase.Type)
	var got []string
	for _, v := range phase.Enum {
		var s string
		r.NoError(yaml.Unmarshal(v.Raw, &s))
		got = append(got, s)
	}
	r.ElementsMatch(lifecyclePhaseEnum, got)
}

// printColumnNames returns the v1beta1 printer column names split by priority:
// the default kubectl get set and the -o wide set.
func printColumnNames(t GinkgoTInterface, crd *apiextv1.CustomResourceDefinition) (defaultCols, wideCols map[string]bool) {
	t.Helper()
	defaultCols = map[string]bool{}
	wideCols = map[string]bool{}
	for _, c := range v1beta1Version(t, crd).AdditionalPrinterColumns {
		if c.Priority == 0 {
			defaultCols[c.Name] = true
		} else {
			wideCols[c.Name] = true
		}
	}
	return defaultCols, wideCols
}

// assertRevisionCRDShape checks what the two revision kinds share: namespaced,
// in the oam category, no status (snapshots are immutable and have nothing to
// report), and a spec that requires the source name, the content hash and the
// snapshotted list.
func assertRevisionCRDShape(t GinkgoTInterface, crd *apiextv1.CustomResourceDefinition, plural, nameField, listField string) apiextv1.JSONSchemaProps {
	t.Helper()
	r := require.New(t)
	r.Equal(apiextv1.NamespaceScoped, crd.Spec.Scope)
	r.Equal(plural, crd.Spec.Names.Plural)
	r.Contains(crd.Spec.Names.Categories, "oam")

	version := v1beta1Version(t, crd)
	r.NotNil(version.Subresources)
	r.Nil(version.Subresources.Status, "revisions are immutable snapshots with no status")

	spec := v1beta1Schema(t, crd).Properties["spec"]
	r.ElementsMatch([]string{nameField, "hash", listField}, spec.Required)
	r.NotContains(spec.Required, "description")
	return spec
}

// A ClusterPlaneRevision freezes a ClusterPlane's components under a content
// hash, so the spoke can be handed exactly what the hub rendered against.
var _ = It("ClusterPlaneRevisionCRD IsImmutableSnapshotOfComponents", func() {
	t := GinkgoT()
	r := require.New(t)
	crd := loadCRD(t, clusterPlaneRevisionCRDPath)
	spec := assertRevisionCRDShape(t, crd, "clusterplanerevisions", "planeName", "components")

	component := spec.Properties["components"].Items.Schema
	r.ElementsMatch([]string{"name", "type"}, component.Required)
	r.NotNil(component.Properties["properties"].XPreserveUnknownFields,
		"snapshotted component properties stay verbatim like on ClusterPlane")

	defaultCols, _ := printColumnNames(t, crd)
	for _, name := range []string{"PLANE", "HASH", "AGE"} {
		r.Truef(defaultCols[name], "expected default column %q", name)
	}
})

// A ClusterBlueprintRevision freezes the blueprint's plane list, each plane
// pinned to a ClusterPlaneRevision name and tagged with its lifecycle phase.
var _ = It("ClusterBlueprintRevisionCRD PinsPlanesToPlaneRevisions", func() {
	t := GinkgoT()
	r := require.New(t)
	crd := loadCRD(t, clusterBlueprintRevisionCRDPath)
	spec := assertRevisionCRDShape(t, crd, "clusterblueprintrevisions", "blueprintName", "planes")

	plane := spec.Properties["planes"].Items.Schema
	r.ElementsMatch([]string{"name", "phase", "planeRevision"}, plane.Required,
		"a snapshot records the phase explicitly instead of relying on the default")
	r.NotContains(plane.Required, "dependsOn")
	assertLifecyclePhaseEnum(t, plane.Properties["phase"])
	r.Equal("string", plane.Properties["planeRevision"].Type)
	r.Equal(dns1123LabelPattern, plane.Properties["name"].Pattern,
		"the plane name becomes the spoke Application name cluster-<name>, so it has to be a DNS-1123 label")
	r.Nil(plane.Properties["name"].MaxLength, "the 31-character cap was enforced on the source ClusterBlueprint")

	defaultCols, _ := printColumnNames(t, crd)
	for _, name := range []string{"BLUEPRINT", "HASH", "AGE"} {
		r.Truef(defaultCols[name], "expected default column %q", name)
	}
})

// The spoke-side Cluster carries one blueprint revision per lifecycle phase and
// reports its status through the subresource, which is what the hub pulls.
var _ = It("ClusterCRD OneBlueprintRefPerPhaseWithStatus", func() {
	t := GinkgoT()
	r := require.New(t)
	crd := loadCRD(t, clusterCRDPath)
	r.Equal(apiextv1.NamespaceScoped, crd.Spec.Scope)
	r.Equal("clusters", crd.Spec.Names.Plural)
	r.Contains(crd.Spec.Names.Categories, "oam")

	version := v1beta1Version(t, crd)
	r.NotNil(version.Subresources)
	r.NotNil(version.Subresources.Status, "the hub pulls Cluster status, so it must be a subresource")

	schema := v1beta1Schema(t, crd)
	spec := schema.Properties["spec"]
	r.Empty(spec.Required, "every phase is optional on its own")
	for _, phase := range lifecyclePhaseEnum {
		ps := spec.Properties[phase]
		r.Equalf([]string{"blueprintRef"}, ps.Required, "phase %s must require blueprintRef", phase)
		r.Equalf([]string{"name"}, ps.Properties["blueprintRef"].Required, "phase %s blueprintRef must require name", phase)
	}
	var atLeastOne bool
	for _, rule := range spec.XValidations {
		if strings.Contains(rule.Rule, "has(self.clusterInit)") &&
			strings.Contains(rule.Rule, "has(self.planeProvisioning)") &&
			strings.Contains(rule.Rule, "has(self.healthValidation)") {
			atLeastOne = true
		}
	}
	r.True(atLeastOne, "spec needs a CEL rule requiring at least one phase ref")

	status := schema.Properties["status"]
	for _, field := range []string{"observedGeneration", "phase", "conditions", "planes", "health"} {
		r.Contains(status.Properties, field)
	}
	var phases []string
	for _, v := range status.Properties["phase"].Enum {
		var s string
		r.NoError(yaml.Unmarshal(v.Raw, &s))
		phases = append(phases, s)
	}
	r.ElementsMatch([]string{"Pending", "Initializing", "Provisioning", "Validating", "Ready", "Degraded"}, phases)
	planeStatus := status.Properties["planes"].Items.Schema
	r.ElementsMatch([]string{"name", "phase", "application", "healthy"}, planeStatus.Required)
	assertLifecyclePhaseEnum(t, planeStatus.Properties["phase"])
	r.Contains(planeStatus.Properties, "planeRevision", "status records which ClusterPlaneRevision each plane was rendered from")
	r.Equal("string", planeStatus.Properties["planeRevision"].Type)
	r.NotContains(planeStatus.Required, "planeRevision", "a plane whose revision never arrived has none to report")
	health := status.Properties["health"]
	r.ElementsMatch([]string{"status", "planesHealthy", "planesTotal"}, health.Required)
	r.Contains(health.Properties, "lastCheckTime")

	defaultCols, _ := printColumnNames(t, crd)
	for _, name := range []string{"PHASE", "HEALTHY", "PLANES", "AGE"} {
		r.Truef(defaultCols[name], "expected default column %q", name)
	}
})
