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

package defkit_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oam-dev/kubevela/pkg/definition/defkit"
	"github.com/oam-dev/kubevela/pkg/defschematic/ir"
	"github.com/oam-dev/kubevela/pkg/defschematic/pocdefs"
)

func TestToDefkitWebserviceGolden(t *testing.T) {
	d, err := pocdefs.DefkitWebservice().ToDefkit()
	require.NoError(t, err)
	require.Equal(t, ir.KindComponent, d.Kind)
	require.Equal(t, "defkit-webservice", d.Name)
	require.Equal(t, ir.APIVersion, d.APIVersion)
	require.NotNil(t, d.Template.Output)
	require.Equal(t, "Deployment", d.Template.Output.Kind)
	require.Contains(t, d.Template.Outputs, "service")

	b, err := d.ToJSON()
	require.NoError(t, err)
	var raw map[string]interface{}
	require.NoError(t, json.Unmarshal(b, &raw))
	require.Equal(t, "defkit.oam.dev/v1alpha1", raw["apiVersion"])
}

func TestToDefkitScalerYAML(t *testing.T) {
	y, err := pocdefs.DefkitScaler().ToDefkitYAML()
	require.NoError(t, err)
	require.Contains(t, string(y), "schematic:")
	require.Contains(t, string(y), "defkit:")
	require.Contains(t, string(y), "defkit-scaler")
	require.NotContains(t, string(y), "schematic:\n  cue:")
}

func TestToDefkitPassStep(t *testing.T) {
	d, err := pocdefs.DefkitPassStep().ToDefkit()
	require.NoError(t, err)
	require.Equal(t, ir.KindWorkflowStep, d.Kind)
	require.Contains(t, d.Workflow.Outputs, "echo")
}

func TestToCueStillWorks(t *testing.T) {
	cue := pocdefs.DefkitWebservice().ToCue()
	require.Contains(t, cue, "parameter:")
	require.Contains(t, cue, "output:")
}

func TestRegistryEmitModeDefkit(t *testing.T) {
	defkit.Clear()
	defkit.Register(pocdefs.DefkitScaler())
	t.Setenv("DEFKIT_EMIT", "defkit")
	b, err := defkit.ToJSON()
	require.NoError(t, err)
	var out defkit.RegistryOutput
	require.NoError(t, json.Unmarshal(b, &out))
	require.Len(t, out.Definitions, 1)
	require.NotEmpty(t, out.Definitions[0].Defkit)
	require.Empty(t, out.Definitions[0].CUE)
	_ = os.Unsetenv("DEFKIT_EMIT")
	defkit.Clear()
}
