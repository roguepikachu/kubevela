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

package eval

import (
	"encoding/json"
	"fmt"
	"strings"

	"cuelang.org/go/cue/cuecontext"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubevela/workflow/pkg/cue/model"
	"github.com/kubevela/workflow/pkg/cue/process"

	"github.com/oam-dev/kubevela/pkg/cue/definition"
	"github.com/oam-dev/kubevela/pkg/cue/definition/health"
	"github.com/oam-dev/kubevela/pkg/defschematic/ir"
	"github.com/oam-dev/kubevela/pkg/oam/util"
)

// Engine implements definition.AbstractEngine using defkit schematic evaluation.
// model.Instance wrapping uses a thin CUE compile of already-rendered JSON
// solely to satisfy process.Context SetBase/AppendAuxiliaries; definition
// logic itself does not execute CUE templates.
type Engine struct {
	name    string
	isTrait bool
}

// NewWorkloadEngine returns a DIR workload/policy engine.
func NewWorkloadEngine(name string) definition.AbstractEngine {
	return &Engine{name: name, isTrait: false}
}

// NewTraitEngine returns a DIR trait engine.
func NewTraitEngine(name string) definition.AbstractEngine {
	return &Engine{name: name, isTrait: true}
}

// Complete renders DIR template into the process context.
func (e *Engine) Complete(ctx process.Context, abstractTemplate string, params interface{}) error {
	def, err := ir.ParseJSON([]byte(abstractTemplate))
	if err != nil {
		return errors.WithMessagef(err, "parse defkit for %s", e.name)
	}
	paramMap, err := toMap(params)
	if err != nil {
		return err
	}
	ctxMap, err := contextMap(ctx)
	if err != nil {
		return err
	}

	if e.isTrait {
		baseInst, aux := ctx.Output()
		baseRes := &Result{Outputs: map[string]*unstructured.Unstructured{}}
		if baseInst != nil {
			u, err := baseInst.Unstructured()
			if err != nil {
				return err
			}
			baseRes.Output = u
		}
		for _, a := range aux {
			if a.Ins == nil {
				continue
			}
			u, err := a.Ins.Unstructured()
			if err != nil {
				return err
			}
			baseRes.Outputs[a.Name] = u
		}
		res, err := EvalTrait(def, paramMap, ctxMap, baseRes)
		if err != nil {
			return errors.WithMessagef(err, "eval defkit trait %s", e.name)
		}
		if res.Output != nil {
			inst, err := instanceFromUnstructured(res.Output, true)
			if err != nil {
				return err
			}
			if err := ctx.SetBase(inst); err != nil {
				return err
			}
		}
		for name, u := range res.Outputs {
			inst, err := instanceFromUnstructured(u, false)
			if err != nil {
				return err
			}
			if err := ctx.AppendAuxiliaries(process.Auxiliary{Ins: inst, Type: e.name, Name: name}); err != nil {
				return err
			}
		}
		return nil
	}

	res, err := EvalDefinition(def, paramMap, ctxMap)
	if err != nil {
		return errors.WithMessagef(err, "eval defkit workload %s", e.name)
	}
	inst, err := instanceFromUnstructured(res.Output, true)
	if err != nil {
		return err
	}
	if err := ctx.SetBase(inst); err != nil {
		return err
	}
	for name, u := range res.Outputs {
		other, err := instanceFromUnstructured(u, false)
		if err != nil {
			return err
		}
		if err := ctx.AppendAuxiliaries(process.Auxiliary{
			Ins:  other,
			Type: definition.AuxiliaryWorkload,
			Name: name,
		}); err != nil {
			return err
		}
	}
	return nil
}

// Status evaluates native health/status from the defkit schematic.
func (e *Engine) Status(templateContext map[string]interface{}, request *health.StatusRequest) (*health.StatusResult, error) {
	var def *ir.Definition
	var err error
	if request != nil && strings.TrimSpace(request.Health) != "" && strings.Contains(request.Health, "defkit.oam.dev/") {
		def, err = ir.ParseJSON([]byte(request.Health))
		if err != nil {
			return nil, err
		}
	} else if raw, ok := templateContext["_defkit"].(string); ok && raw != "" {
		def, err = ir.ParseJSON([]byte(raw))
		if err != nil {
			return nil, err
		}
	} else if d, ok := templateContext["_defkitDef"].(*ir.Definition); ok {
		def = d
	}
	if def == nil || def.Health == nil {
		return &health.StatusResult{Healthy: true, Message: "defkit-poc: status eval not implemented"}, nil
	}
	ctx := templateContext
	if request != nil && request.Parameter != nil {
		if ctx == nil {
			ctx = map[string]interface{}{}
		}
		ctx["parameter"] = request.Parameter
	}
	healthy, message, err := EvalHealth(def, ctx)
	if err != nil {
		return nil, err
	}
	return &health.StatusResult{Healthy: healthy, Message: message}, nil
}

// GetTemplateContext reuses the workload engine's cluster fetch by delegating
// to a temporary CUE workload engine for live object loading only.
func (e *Engine) GetTemplateContext(ctx process.Context, cli client.Client, accessor util.NamespaceAccessor) (map[string]interface{}, error) {
	return definition.NewWorkloadAbstractEngine(e.name).GetTemplateContext(ctx, cli, accessor)
}

func toMap(params interface{}) (map[string]interface{}, error) {
	if params == nil {
		return map[string]interface{}{}, nil
	}
	if m, ok := params.(map[string]interface{}); ok {
		return m, nil
	}
	b, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	out := map[string]interface{}{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func contextMap(ctx process.Context) (map[string]interface{}, error) {
	// Avoid BaseContextFile (CUE marshal of model.Instance). Pull known keys from process data.
	out := map[string]interface{}{}
	keys := []string{
		"name", "namespace", "appName", "appRevision", "appRevisionNum",
		"cluster", "clusterVersion", "components", "revision", "replicaKey",
		"appLabels", "appAnnotations", "workflowName", "publishVersion",
	}
	for _, k := range keys {
		if v := ctx.GetData(k); v != nil {
			out[k] = v
		}
	}
	return out, nil
}

func instanceFromUnstructured(u *unstructured.Unstructured, base bool) (model.Instance, error) {
	if u == nil {
		return nil, fmt.Errorf("nil unstructured")
	}
	// Encode Go map directly; CompileBytes JSON can produce values that fail MarshalJSON
	// in later BaseContextFile calls under Cue 0.14.
	v := cuecontext.New().Encode(u.Object)
	if v.Err() != nil {
		return nil, v.Err()
	}
	if base {
		return model.NewBase(v)
	}
	return model.NewOther(v)
}
