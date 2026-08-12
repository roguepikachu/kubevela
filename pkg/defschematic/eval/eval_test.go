/*
Copyright 2026 The KubeVela Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

    20|Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package eval_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oam-dev/kubevela/pkg/defschematic/cuebridge"
	"github.com/oam-dev/kubevela/pkg/defschematic/eval"
	"github.com/oam-dev/kubevela/pkg/defschematic/ir"
	"github.com/oam-dev/kubevela/pkg/defschematic/pocdefs"
)

func TestEvalComponentNative(t *testing.T) {
	def := pocdefs.MustDefkitIR(pocdefs.DefkitWebservice())
	res, err := eval.EvalDefinition(def, map[string]interface{}{
		"image": "nginx:1.27-alpine",
	}, map[string]interface{}{
		"name":      "demo",
		"namespace": "default",
	})
	require.NoError(t, err)
	require.Equal(t, "Deployment", res.Output.GetKind())
	require.Equal(t, "demo", res.Output.GetName())
	replicas, found, err := unstructuredNestedInt(res.Output.Object, "spec", "replicas")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(1), replicas)
	svc, ok := res.Outputs["service"]
	require.True(t, ok)
	require.Equal(t, "Service", svc.GetKind())
	require.Equal(t, "demo-svc", svc.GetName())
}

func TestEvalTraitPatch(t *testing.T) {
	comp := pocdefs.MustDefkitIR(pocdefs.DefkitWebservice())
	base, err := eval.EvalDefinition(comp, map[string]interface{}{
		"image": "nginx:1.27-alpine",
	}, map[string]interface{}{"name": "demo", "namespace": "default"})
	require.NoError(t, err)

	trait := pocdefs.MustDefkitIR(pocdefs.DefkitScaler())
	out, err := eval.EvalTrait(trait, map[string]interface{}{"replicas": 3}, map[string]interface{}{
		"name": "demo", "namespace": "default",
	}, base)
	require.NoError(t, err)
	replicas, found, err := unstructuredNestedInt(out.Output.Object, "spec", "replicas")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, int64(3), replicas)
}

func TestEvalPolicy(t *testing.T) {
	def := pocdefs.MustDefkitIR(pocdefs.DefkitOverridePolicy())
	res, err := eval.EvalDefinition(def, map[string]interface{}{
		"components": "frontend,backend",
	}, map[string]interface{}{"name": "demo", "namespace": "default"})
	require.NoError(t, err)
	require.Equal(t, "ConfigMap", res.Output.GetKind())
	data, _, _ := unstructuredNestedString(res.Output.Object, "data", "engine")
	require.Equal(t, "defkit", data)
}

func TestEvalWorkflowStepNative(t *testing.T) {
	def := pocdefs.MustDefkitIR(pocdefs.DefkitPassStep())
	res, err := eval.EvalWorkflowStep(def, map[string]interface{}{"prefix": "hi"}, map[string]interface{}{
		"message": "world",
	}, map[string]interface{}{})
	require.NoError(t, err)
	require.Equal(t, "hi:world", res.Outputs["echo"])
	require.Contains(t, res.Message, "world")
}

func TestParamValidation(t *testing.T) {
	def := pocdefs.MustDefkitIR(pocdefs.DefkitWebservice())
	_, err := eval.EvalDefinition(def, map[string]interface{}{}, map[string]interface{}{"name": "x", "namespace": "default"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "required")
}

func TestCueBridgeSmoke(t *testing.T) {
	def := pocdefs.MustDefkitIR(pocdefs.DefkitWebservice())
	cue, err := cuebridge.ToCUE(def)
	require.NoError(t, err)
	require.Contains(t, cue, "parameter:")
	require.Contains(t, cue, "output:")
}

func TestGoldenComponentJSON(t *testing.T) {
	def := pocdefs.MustDefkitIR(pocdefs.DefkitWebservice())
	res, err := eval.EvalDefinition(def, map[string]interface{}{
		"image":    "nginx:1.27-alpine",
		"replicas": 2,
		"port":     8080,
	}, map[string]interface{}{"name": "web", "namespace": "demo"})
	require.NoError(t, err)
	got, err := json.MarshalIndent(res.Output.Object, "", "  ")
	require.NoError(t, err)

	golden := filepath.Join("testdata", "defkit-webservice-output.json")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(golden, got, 0o644))
	}
	want, err := os.ReadFile(golden)
	if os.IsNotExist(err) {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(golden, got, 0o644))
		want = got
	} else {
		require.NoError(t, err)
	}
	require.JSONEq(t, string(want), string(got))
}

func TestParseRoundTrip(t *testing.T) {
	def := pocdefs.MustDefkitIR(pocdefs.DefkitWebservice())
	b, err := def.ToJSON()
	require.NoError(t, err)
	parsed, err := ir.ParseJSON(b)
	require.NoError(t, err)
	require.Equal(t, def.Name, parsed.Name)
	require.Equal(t, ir.KindComponent, parsed.Kind)
}

func unstructuredNestedInt(obj map[string]interface{}, fields ...string) (int64, bool, error) {
	cur := interface{}(obj)
	for _, f := range fields {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return 0, false, nil
		}
		cur, ok = m[f]
		if !ok {
			return 0, false, nil
		}
	}
	switch n := cur.(type) {
	case int64:
		return n, true, nil
	case float64:
		return int64(n), true, nil
	case int:
		return int64(n), true, nil
	default:
		return 0, false, nil
	}
}

func unstructuredNestedString(obj map[string]interface{}, fields ...string) (string, bool, error) {
	cur := interface{}(obj)
	for _, f := range fields {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return "", false, nil
		}
		cur, ok = m[f]
		if !ok {
			return "", false, nil
		}
	}
	s, ok := cur.(string)
	return s, ok, nil
}
