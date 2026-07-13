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
	"testing"

	"github.com/stretchr/testify/require"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

const spokeClusterCRDPath = "../../../charts/vela-core/crds/core.oam.dev_spokeclusters.yaml"

func loadSpokeClusterCRD(t *testing.T) *apiextv1.CustomResourceDefinition {
	t.Helper()
	raw, err := os.ReadFile(spokeClusterCRDPath)
	require.NoError(t, err, "generated SpokeCluster CRD must be present in charts/vela-core/crds")
	crd := &apiextv1.CustomResourceDefinition{}
	require.NoError(t, yaml.Unmarshal(raw, crd))
	return crd
}

// v1beta1Schema returns the openAPIV3 schema of the v1beta1 version.
func v1beta1Schema(t *testing.T, crd *apiextv1.CustomResourceDefinition) *apiextv1.JSONSchemaProps {
	t.Helper()
	for _, v := range crd.Spec.Versions {
		if v.Name == "v1beta1" {
			require.NotNil(t, v.Schema)
			return v.Schema.OpenAPIV3Schema
		}
	}
	t.Fatalf("v1beta1 version not found in CRD")
	return nil
}

// TestSpokeClusterCRD_Namespaced asserts the installed CRD is namespaced
// and named to avoid the Cluster API collision (Requirement 1).
func TestSpokeClusterCRD_Namespaced(t *testing.T) {
	r := require.New(t)
	crd := loadSpokeClusterCRD(t)

	r.Equal(apiextv1.NamespaceScoped, crd.Spec.Scope)
	r.Equal("spokeclusters", crd.Spec.Names.Plural)
	r.Equal("SpokeCluster", crd.Spec.Names.Kind)
	r.Equal("core.oam.dev", crd.Spec.Group)

	// Kubectl surface: sc and spc short names, and the oam category shared by
	// every core.oam.dev CRD so `kubectl get oam` includes SpokeCluster.
	r.ElementsMatch([]string{"sc", "spc"}, crd.Spec.Names.ShortNames)
	r.Contains(crd.Spec.Names.Categories, "oam")
}

// TestSpokeClusterCRD_PrinterColumns asserts the default and wide fleet-summary
// columns are present (Requirement 5).
func TestSpokeClusterCRD_PrinterColumns(t *testing.T) {
	r := require.New(t)
	crd := loadSpokeClusterCRD(t)

	var version *apiextv1.CustomResourceDefinitionVersion
	for i := range crd.Spec.Versions {
		if crd.Spec.Versions[i].Name == "v1beta1" {
			version = &crd.Spec.Versions[i]
		}
	}
	r.NotNil(version)

	defaultCols := map[string]bool{}
	wideCols := map[string]bool{}
	for _, c := range version.AdditionalPrinterColumns {
		if c.Priority == 0 {
			defaultCols[c.Name] = true
		} else {
			wideCols[c.Name] = true
		}
	}

	// NAME and AGE: NAME is implicit; AGE is an explicit column here.
	for _, name := range []string{"MODE", "VERSION", "NODES", "PLATFORM", "STATUS", "AGE"} {
		r.Truef(defaultCols[name], "expected default column %q", name)
	}
	for _, name := range []string{"REGION", "ENDPOINT", "CPU", "MEMORY", "LATENCY", "AUTH", "LAST PROBE"} {
		r.Truef(wideCols[name], "expected wide (priority>0) column %q", name)
	}
}

// TestSpokeClusterCRD_Enums asserts the closed value sets are enforced at the
// schema level (Requirements 2, 3, 4).
func TestSpokeClusterCRD_Enums(t *testing.T) {
	r := require.New(t)
	schema := v1beta1Schema(t, loadSpokeClusterCRD(t))

	enumValues := func(props apiextv1.JSONSchemaProps) []string {
		out := make([]string, 0, len(props.Enum))
		for _, e := range props.Enum {
			// Enum JSON values are quoted strings, e.g. "connect".
			out = append(out, string(e.Raw))
		}
		return out
	}

	spec := schema.Properties["spec"]
	r.ElementsMatch([]string{`"connect"`, `"provision"`, `"adopt"`}, enumValues(spec.Properties["mode"]))
	r.ElementsMatch([]string{`"detach"`, `"orphan"`}, enumValues(spec.Properties["deletionPolicy"]))

	credential := spec.Properties["credential"]
	r.ElementsMatch([]string{`"kubeconfig"`, `"aws"`, `"azure"`, `"gcp"`}, enumValues(credential.Properties["type"]))

	aws := credential.Properties["aws"]
	r.ElementsMatch([]string{`"podIdentity"`, `"irsa"`}, enumValues(aws.Properties["authMode"]))

	// The azure and gcp arms are Phase 1 placeholders with no provider behind
	// them, but the union still carries their auth-mode enums so a later
	// provider inherits a stable schema.
	azure := credential.Properties["azure"]
	r.ElementsMatch([]string{`"workloadIdentity"`, `"managedIdentity"`}, enumValues(azure.Properties["authMode"]))

	gcp := credential.Properties["gcp"]
	r.ElementsMatch([]string{`"workloadIdentityFederation"`, `"serviceAccount"`}, enumValues(gcp.Properties["authMode"]))

	status := schema.Properties["status"]
	r.ElementsMatch([]string{`"Connected"`, `"Disconnected"`, `"Unknown"`}, enumValues(status.Properties["connection"]))
}

// TestSpokeClusterCRD_OptionalSecretNamespace asserts the kubeconfig Secret
// reference only requires a name; namespace is optional and cross-namespace
// references are rejected by the webhook's default policy later
// (Requirement 2, criterion 2).
func TestSpokeClusterCRD_OptionalSecretNamespace(t *testing.T) {
	r := require.New(t)
	schema := v1beta1Schema(t, loadSpokeClusterCRD(t))

	secretRef := schema.
		Properties["spec"].
		Properties["credential"].
		Properties["kubeconfig"].
		Properties["secretRef"]

	r.Contains(secretRef.Required, "name")
	r.NotContains(secretRef.Required, "namespace")
}

// TestSpokeClusterCRD_AuthColumnFromSpec asserts the wide AUTH column reads the
// credential type from spec, not a status field (there is no status.authMethod).
func TestSpokeClusterCRD_AuthColumnFromSpec(t *testing.T) {
	r := require.New(t)
	crd := loadSpokeClusterCRD(t)

	var version *apiextv1.CustomResourceDefinitionVersion
	for i := range crd.Spec.Versions {
		if crd.Spec.Versions[i].Name == "v1beta1" {
			version = &crd.Spec.Versions[i]
		}
	}
	r.NotNil(version)

	var authPath string
	for _, c := range version.AdditionalPrinterColumns {
		if c.Name == "AUTH" {
			authPath = c.JSONPath
		}
	}
	r.Equal(".spec.credential.type", authPath)
}

// TestSpokeClusterCRD_Phase2Stubs asserts the forward-compatible Phase 2 fields
// are present in the schema with their intended shape. No Phase 1 controller
// reconciles them, but they are published now so the dispatch and rollout work
// in a later phase does not require a breaking CRD change.
func TestSpokeClusterCRD_Phase2Stubs(t *testing.T) {
	r := require.New(t)
	schema := v1beta1Schema(t, loadSpokeClusterCRD(t))

	spec := schema.Properties["spec"]
	status := schema.Properties["status"]

	// spec.infraProvisioning.blueprintRef is a name/revision BlueprintReference.
	infra := spec.Properties["infraProvisioning"]
	infraBlueprint := infra.Properties["blueprintRef"]
	r.Contains(infraBlueprint.Properties, "name")
	r.Contains(infraBlueprint.Properties, "revision")
	r.Equal([]string{"name"}, infraBlueprint.Required)

	// status.dispatchedRevision is the revision the dispatch loop compares against
	// spec.blueprintRef.revision.
	r.Contains(status.Properties, "dispatchedRevision")
	r.Equal("string", status.Properties["dispatchedRevision"].Type)

	// status.health summarizes the blueprint health pulled from the spoke.
	health := status.Properties["health"]
	for _, field := range []string{"status", "planesHealthy", "planesTotal", "lastPulledAt"} {
		r.Contains(health.Properties, field)
	}
}
