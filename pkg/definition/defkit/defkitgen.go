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

package defkit

import (
	"fmt"
	"os"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/oam-dev/kubevela/pkg/defschematic/ir"
)

// EmitMode controls what Register / ToJSON produces for reusable Go modules.
type EmitMode string

const (
	// EmitCUE fills DefinitionOutput.CUE (default, compatibility).
	EmitCUE EmitMode = "cue"
	// EmitDefkit fills DefinitionOutput.Defkit with schematic.defkit JSON.
	EmitDefkit EmitMode = "defkit"
)

// EmitModeFromEnv reads DEFKIT_EMIT (cue|defkit). Empty/unknown defaults to cue.
func EmitModeFromEnv() EmitMode {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("DEFKIT_EMIT"))) {
	case string(EmitDefkit):
		return EmitDefkit
	default:
		return EmitCUE
	}
}

// ToDefkit lowers this component into a defkit schematic IR document.
func (c *ComponentDefinition) ToDefkit() (*ir.Definition, error) {
	if c.HasRawCUE() {
		return nil, fmt.Errorf("ToDefkit: RawCUE is not supported; use ToCue() for CUE emit")
	}
	tpl, err := materializeTemplate(c.GetTemplate())
	if err != nil {
		return nil, err
	}
	params, err := lowerParams(c.GetParams())
	if err != nil {
		return nil, err
	}
	tmpl, err := lowerTemplate(tpl)
	if err != nil {
		return nil, err
	}
	helpers, err := lowerClaimNames(tpl)
	if err != nil {
		return nil, err
	}
	when, err := lowerConditionalParamBlocks(c.GetConditionalParamBlocks())
	if err != nil {
		return nil, err
	}
	validators, err := lowerValidators(c.GetValidators(), "")
	if err != nil {
		return nil, err
	}
	// Hoist nested MapParam/Object validators (e.g. governance field checks).
	for _, p := range c.GetParams() {
		if mp, ok := p.(*MapParam); ok {
			vs, err := lowerValidators(mp.GetValidators(), mp.Name())
			if err != nil {
				return nil, err
			}
			validators = append(validators, vs...)
		}
	}
	return &ir.Definition{
		APIVersion: ir.APIVersion,
		Kind:       ir.KindComponent,
		Name:       c.GetName(),
		Params:     params,
		When:       when,
		Validators: validators,
		Helpers:    helpers,
		Health:     c.GetSchematicHealth(),
		Status:     c.GetSchematicStatus(),
		Template:   tmpl,
	}, nil
}

// ToDefkitJSON returns the JSON schematic.defkit.template payload.
func (c *ComponentDefinition) ToDefkitJSON() (string, error) {
	d, err := c.ToDefkit()
	if err != nil {
		return "", err
	}
	b, err := d.ToJSON()
	return string(b), err
}

// ToDefkitYAML returns a ComponentDefinition CR with schematic.defkit.
func (c *ComponentDefinition) ToDefkitYAML() ([]byte, error) {
	jsonStr, err := c.ToDefkitJSON()
	if err != nil {
		return nil, err
	}
	cr := map[string]any{
		"apiVersion": "core.oam.dev/v1beta1",
		"kind":       "ComponentDefinition",
		"metadata": map[string]any{
			"name": c.GetName(),
			"annotations": func() map[string]any {
				a := map[string]any{}
				for k, v := range c.GetAnnotations() {
					a[k] = v
				}
				if c.GetDescription() != "" {
					a["definition.oam.dev/description"] = c.GetDescription()
				}
				return a
			}(),
		},
		"spec": map[string]any{
			"workload": map[string]any{
				"definition": map[string]any{
					"apiVersion": c.workload.apiVersion,
					"kind":       c.workload.kind,
				},
			},
			"schematic": map[string]any{
				"defkit": map[string]any{
					"template": jsonStr,
				},
			},
		},
	}
	if c.workload.autodetect {
		cr["spec"].(map[string]any)["workload"] = map[string]any{
			"type": "autodetects.core.oam.dev",
		}
	}
	if c.GetVersion() != "" {
		cr["spec"].(map[string]any)["version"] = c.GetVersion()
	}
	return yaml.Marshal(cr)
}

// ToDefkit lowers this trait into a defkit schematic IR document.
func (t *TraitDefinition) ToDefkit() (*ir.Definition, error) {
	if t.HasRawCUE() {
		return nil, fmt.Errorf("ToDefkit: RawCUE is not supported; use ToCue() for CUE emit")
	}
	tpl, err := materializeTemplate(t.GetTemplate())
	if err != nil {
		return nil, err
	}
	params, err := lowerParams(t.GetParams())
	if err != nil {
		return nil, err
	}
	tmpl, err := lowerTemplate(tpl)
	if err != nil {
		return nil, err
	}
	return &ir.Definition{
		APIVersion: ir.APIVersion,
		Kind:       ir.KindTrait,
		Name:       t.GetName(),
		Params:     params,
		Template:   tmpl,
	}, nil
}

// ToDefkitJSON returns the JSON schematic.defkit.template payload.
func (t *TraitDefinition) ToDefkitJSON() (string, error) {
	d, err := t.ToDefkit()
	if err != nil {
		return "", err
	}
	b, err := d.ToJSON()
	return string(b), err
}

// ToDefkitYAML returns a TraitDefinition CR with schematic.defkit.
func (t *TraitDefinition) ToDefkitYAML() ([]byte, error) {
	jsonStr, err := t.ToDefkitJSON()
	if err != nil {
		return nil, err
	}
	applies := t.GetAppliesToWorkloads()
	if len(applies) == 0 {
		applies = []string{"*"}
	}
	cr := map[string]any{
		"apiVersion": "core.oam.dev/v1beta1",
		"kind":       "TraitDefinition",
		"metadata": map[string]any{
			"name": t.GetName(),
			"annotations": func() map[string]any {
				a := map[string]any{}
				for k, v := range t.GetAnnotations() {
					a[k] = v
				}
				if t.GetDescription() != "" {
					a["definition.oam.dev/description"] = t.GetDescription()
				}
				return a
			}(),
		},
		"spec": map[string]any{
			"appliesToWorkloads": applies,
			"schematic": map[string]any{
				"defkit": map[string]any{
					"template": jsonStr,
				},
			},
		},
	}
	if t.GetVersion() != "" {
		cr["spec"].(map[string]any)["version"] = t.GetVersion()
	}
	return yaml.Marshal(cr)
}

// ToDefkit lowers this policy into a defkit schematic IR document.
func (p *PolicyDefinition) ToDefkit() (*ir.Definition, error) {
	if p.HasRawCUE() {
		return nil, fmt.Errorf("ToDefkit: RawCUE is not supported; use ToCue() for CUE emit")
	}
	params, err := lowerParams(p.GetParams())
	if err != nil {
		return nil, err
	}
	def := &ir.Definition{
		APIVersion: ir.APIVersion,
		Kind:       ir.KindPolicy,
		Name:       p.GetName(),
		Params:     params,
		Policy:     &ir.Policy{},
	}
	if p.policyTemplate != nil {
		pt := NewPolicyTemplate()
		p.policyTemplate(pt)
		if pt.output != nil {
			res, err := lowerResource(pt.output)
			if err != nil {
				return nil, err
			}
			def.Policy.Output = res
		}
		if len(pt.computedFields) > 0 {
			data := map[string]ir.Expr{}
			for k, v := range pt.computedFields {
				ex, err := lowerValue(v)
				if err != nil {
					return nil, fmt.Errorf("policy field %q: %w", k, err)
				}
				data[k] = ex
			}
			def.Policy.Data = data
		}
	}
	return def, nil
}

// ToDefkitJSON returns the JSON schematic.defkit.template payload.
func (p *PolicyDefinition) ToDefkitJSON() (string, error) {
	d, err := p.ToDefkit()
	if err != nil {
		return "", err
	}
	b, err := d.ToJSON()
	return string(b), err
}

// ToDefkitYAML returns a PolicyDefinition CR with schematic.defkit.
func (p *PolicyDefinition) ToDefkitYAML() ([]byte, error) {
	jsonStr, err := p.ToDefkitJSON()
	if err != nil {
		return nil, err
	}
	cr := map[string]any{
		"apiVersion": "core.oam.dev/v1beta1",
		"kind":       "PolicyDefinition",
		"metadata": map[string]any{
			"name": p.GetName(),
			"annotations": func() map[string]any {
				a := map[string]any{}
				for k, v := range p.GetAnnotations() {
					a[k] = v
				}
				if p.GetDescription() != "" {
					a["definition.oam.dev/description"] = p.GetDescription()
				}
				return a
			}(),
		},
		"spec": map[string]any{
			"schematic": map[string]any{
				"defkit": map[string]any{
					"template": jsonStr,
				},
			},
		},
	}
	if p.GetVersion() != "" {
		cr["spec"].(map[string]any)["version"] = p.GetVersion()
	}
	return yaml.Marshal(cr)
}

// ToDefkit lowers this workflow step into a defkit schematic IR document.
func (w *WorkflowStepDefinition) ToDefkit() (*ir.Definition, error) {
	if w.HasRawCUE() || w.HasRawTemplateBody() {
		return nil, fmt.Errorf("ToDefkit: RawCUE/TemplateBody is not supported; use ToCue() for CUE emit")
	}
	params, err := lowerParams(w.GetParams())
	if err != nil {
		return nil, err
	}
	def := &ir.Definition{
		APIVersion: ir.APIVersion,
		Kind:       ir.KindWorkflowStep,
		Name:       w.GetName(),
		Params:     params,
		Workflow:   &ir.Workflow{Outputs: map[string]ir.Expr{}},
	}
	if w.stepTemplate != nil {
		wt := NewWorkflowStepTemplate()
		w.stepTemplate(wt)
		for _, action := range wt.GetActions() {
			va, ok := action.(*ValueAction)
			if !ok {
				return nil, fmt.Errorf("ToDefkit: unsupported workflow action %T (only Set/ValueAction for message/outputs)", action)
			}
			ex, err := lowerValue(va.value)
			if err != nil {
				return nil, fmt.Errorf("workflow field %q: %w", va.name, err)
			}
			switch {
			case va.name == "message":
				def.Workflow.Message = ex
			case strings.HasPrefix(va.name, "outputs."):
				def.Workflow.Outputs[strings.TrimPrefix(va.name, "outputs.")] = ex
			default:
				def.Workflow.Outputs[va.name] = ex
			}
		}
		if msg := wt.GetSuspendMessage(); msg != "" {
			def.Workflow.Message = ir.Expr{Lit: msg}
		}
	}
	return def, nil
}

// ToDefkitJSON returns the JSON schematic.defkit.template payload.
func (w *WorkflowStepDefinition) ToDefkitJSON() (string, error) {
	d, err := w.ToDefkit()
	if err != nil {
		return "", err
	}
	b, err := d.ToJSON()
	return string(b), err
}

// ToDefkitYAML returns a WorkflowStepDefinition CR with schematic.defkit.
func (w *WorkflowStepDefinition) ToDefkitYAML() ([]byte, error) {
	jsonStr, err := w.ToDefkitJSON()
	if err != nil {
		return nil, err
	}
	cr := map[string]any{
		"apiVersion": "core.oam.dev/v1beta1",
		"kind":       "WorkflowStepDefinition",
		"metadata": map[string]any{
			"name": w.GetName(),
			"annotations": func() map[string]any {
				a := map[string]any{}
				for k, v := range w.GetAnnotations() {
					a[k] = v
				}
				if w.GetDescription() != "" {
					a["definition.oam.dev/description"] = w.GetDescription()
				}
				return a
			}(),
		},
		"spec": map[string]any{
			"schematic": map[string]any{
				"defkit": map[string]any{
					"template": jsonStr,
				},
			},
		},
	}
	if w.GetVersion() != "" {
		cr["spec"].(map[string]any)["version"] = w.GetVersion()
	}
	return yaml.Marshal(cr)
}


func materializeTemplate(fn func(tpl *Template)) (*Template, error) {
	if fn == nil {
		return nil, fmt.Errorf("ToDefkit: definition has no Template()")
	}
	tpl := NewTemplate()
	fn(tpl)
	if tpl.rawPatchBlock != "" || tpl.rawParameterBlock != "" || tpl.rawOutputsBlock != "" {
		return nil, fmt.Errorf("ToDefkit: raw CUE template blocks are not supported")
	}
	if tpl.rawHeaderBlock != "" && len(tpl.claimNames) == 0 {
		return nil, fmt.Errorf("ToDefkit: SetRawHeaderBlock is not supported; use Template.ClaimName(...)")
	}
	if tpl.patchContainerConfig != nil {
		return nil, fmt.Errorf("ToDefkit: PatchContainer is not supported")
	}
	if len(tpl.helpers) > 0 || len(tpl.structArrayHelpers) > 0 || len(tpl.concatHelpers) > 0 || len(tpl.dedupeHelpers) > 0 {
		return nil, fmt.Errorf("ToDefkit: helpers/collections are not supported in this PoC subset")
	}
	if len(tpl.letBindings) > 0 {
		return nil, fmt.Errorf("ToDefkit: let bindings are not supported")
	}
	if len(tpl.outputGroups) > 0 {
		return nil, fmt.Errorf("ToDefkit: output groups are not supported")
	}
	return tpl, nil
}

func lowerClaimNames(tpl *Template) ([]ir.Helper, error) {
	var out []ir.Helper
	for _, c := range tpl.claimNames {
		parts := make([]ir.Expr, 0, len(c.Parts))
		for _, p := range c.Parts {
			ex, err := lowerValue(p)
			if err != nil {
				return nil, err
			}
			parts = append(parts, ex)
		}
		maxLen := c.MaxLen
		if maxLen <= 0 {
			maxLen = 63
		}
		out = append(out, ir.Helper{
			Name: c.Name,
			Kind: "claimName",
			ClaimName: &ir.ClaimNameHelper{Parts: parts, MaxLen: maxLen},
		})
	}
	return out, nil
}

func lowerTemplate(tpl *Template) (*ir.Template, error) {
	out := &ir.Template{Outputs: map[string]*ir.Resource{}}
	if tpl.output != nil {
		res, err := lowerResource(tpl.output)
		if err != nil {
			return nil, err
		}
		out.Output = res
	}
	for name, r := range tpl.outputs {
		res, err := lowerResource(r)
		if err != nil {
			return nil, fmt.Errorf("outputs.%s: %w", name, err)
		}
		out.Outputs[name] = res
	}
	if tpl.patch != nil {
		fields, err := lowerPatchOps(tpl.patch.Ops())
		if err != nil {
			return nil, fmt.Errorf("patch: %w", err)
		}
		out.Patches = append(out.Patches, ir.Patch{Target: "output", Fields: fields})
	}
	return out, nil
}

func lowerResource(r *Resource) (*ir.Resource, error) {
	fields, err := lowerFieldOps(r.Ops())
	if err != nil {
		return nil, err
	}
	return &ir.Resource{APIVersion: r.APIVersion(), Kind: r.Kind(), Fields: fields}, nil
}

func lowerFieldOps(ops []ResourceOp) ([]ir.FieldOp, error) {
	var fields []ir.FieldOp
	for _, op := range ops {
		switch o := op.(type) {
		case *SetOp:
			ex, err := lowerValue(o.Value())
			if err != nil {
				return nil, fmt.Errorf("Set(%q): %w", o.Path(), err)
			}
			fields = append(fields, ir.FieldOp{Path: o.Path(), Value: &ex})
		case *SetIfOp:
			ex, err := lowerValue(o.Value())
			if err != nil {
				return nil, fmt.Errorf("SetIf(%q): %w", o.Path(), err)
			}
			cond, err := lowerCondition(o.Cond())
			if err != nil {
				return nil, fmt.Errorf("SetIf(%q) condition: %w", o.Path(), err)
			}
			fields = append(fields, ir.FieldOp{Path: o.Path(), Value: &ex, Condition: cond})
		case *SpreadIfOp:
			ex, err := lowerValue(o.Value())
			if err != nil {
				return nil, fmt.Errorf("SpreadIf(%q): %w", o.Path(), err)
			}
			cond, err := lowerCondition(o.Cond())
			if err != nil {
				return nil, err
			}
			fields = append(fields, ir.FieldOp{Path: o.Path(), Value: &ex, Condition: cond, Spread: true})
		case *ConditionalStructOp:
			cond, err := lowerCondition(o.Cond())
			if err != nil {
				return nil, err
			}
			b := &OutputStructBuilder{}
			o.Builder()(b)
			nested, err := lowerStructBuilderOps(b.Ops())
			if err != nil {
				return nil, err
			}
			fields = append(fields, ir.FieldOp{Path: o.Path(), Condition: cond, StructFields: nested})
		case *IfBlock:
			inner, err := lowerFieldOps(o.Ops())
			if err != nil {
				return nil, err
			}
			cond, err := lowerCondition(o.Cond())
			if err != nil {
				return nil, err
			}
			for _, f := range inner {
				c := *cond
				if f.Condition != nil {
					return nil, fmt.Errorf("nested conditions inside If blocks are not supported")
				}
				f.Condition = &c
				fields = append(fields, f)
			}
		default:
			return nil, fmt.Errorf("ToDefkit: unsupported op %T", op)
		}
	}
	return fields, nil
}

func lowerStructBuilderOps(ops []structBuilderOp) ([]ir.FieldOp, error) {
	var fields []ir.FieldOp
	for _, op := range ops {
		switch o := op.(type) {
		case *structSetOp:
			ex, err := lowerValue(o.value)
			if err != nil {
				return nil, err
			}
			fields = append(fields, ir.FieldOp{Path: o.field, Value: &ex})
		case *structSetIfOp:
			ex, err := lowerValue(o.value)
			if err != nil {
				return nil, err
			}
			cond, err := lowerCondition(o.cond)
			if err != nil {
				return nil, err
			}
			fields = append(fields, ir.FieldOp{Path: o.field, Value: &ex, Condition: cond})
		default:
			return nil, fmt.Errorf("ToDefkit: unsupported struct builder op %T", op)
		}
	}
	return fields, nil
}

func lowerPatchOps(ops []ResourceOp) ([]ir.FieldSet, error) {
	var fields []ir.FieldSet
	for _, op := range ops {
		switch o := op.(type) {
		case *SetOp:
			ex, err := lowerValue(o.Value())
			if err != nil {
				return nil, err
			}
			fields = append(fields, ir.FieldSet{Path: o.Path(), Value: ex})
		case *SetIfOp:
			ex, err := lowerValue(o.Value())
			if err != nil {
				return nil, err
			}
			cond, err := lowerCondition(o.Cond())
			if err != nil {
				return nil, err
			}
			fields = append(fields, ir.FieldSet{Path: o.Path(), Value: ex, Condition: cond})
		default:
			return nil, fmt.Errorf("ToDefkit: unsupported patch op %T", op)
		}
	}
	return fields, nil
}

func lowerConditionalParamBlocks(blocks []*ConditionalParamBlock) ([]ir.ConditionalParamBlock, error) {
	var out []ir.ConditionalParamBlock
	for _, block := range blocks {
		for _, br := range block.Branches() {
			cond, err := lowerCondition(br.Condition())
			if err != nil {
				return nil, err
			}
			params, err := lowerParams(br.GetParams())
			if err != nil {
				return nil, err
			}
			validators, err := lowerValidators(br.GetValidators(), "")
			if err != nil {
				return nil, err
			}
			out = append(out, ir.ConditionalParamBlock{When: cond, Params: params, Validators: validators})
		}
	}
	return out, nil
}

func lowerValidators(validators []*Validator, paramPrefix string) ([]ir.Validator, error) {
	var out []ir.Validator
	for _, v := range validators {
		if v.FailCondition() == nil {
			continue
		}
		// Skip free-form CUEExpr validators for now (lifecycle date math).
		if containsCUEExpr(v.FailCondition()) {
			continue
		}
		fail, err := lowerCondition(v.FailCondition())
		if err != nil {
			return nil, fmt.Errorf("validator %q: %w", v.Message(), err)
		}
		if paramPrefix != "" {
			prefixConditionParams(fail, paramPrefix)
		}
		iv := ir.Validator{Name: v.CUEName(), Message: v.Message(), FailWhen: fail}
		if v.GuardCondition() != nil {
			g, err := lowerCondition(v.GuardCondition())
			if err != nil {
				return nil, err
			}
			if paramPrefix != "" {
				prefixConditionParams(g, paramPrefix)
			}
			iv.OnlyWhen = g
		}
		out = append(out, iv)
	}
	return out, nil
}

func containsCUEExpr(c Condition) bool {
	switch t := c.(type) {
	case *LogicalExpr:
		for _, ch := range t.Conditions() {
			if containsCUEExpr(ch) {
				return true
			}
		}
	case *NotExpr:
		return containsCUEExpr(t.Cond())
	default:
		// cue expr marker types live in collections; detect by type name
		name := fmt.Sprintf("%T", c)
		if strings.Contains(name, "CUEExpr") || strings.Contains(name, "TimeParse") {
			return true
		}
	}
	return false
}

func prefixConditionParams(c *ir.Condition, prefix string) {
	if c == nil {
		return
	}
	if c.Param != "" && !strings.Contains(c.Param, ".") {
		c.Param = prefix + "." + c.Param
	}
	if c.Path != "" && !strings.HasPrefix(c.Path, "parameter.") && !strings.Contains(c.Path, ".") {
		c.Path = "parameter." + prefix + "." + c.Path
	}
	for i := range c.Children {
		prefixConditionParams(&c.Children[i], prefix)
	}
	if c.Child != nil {
		prefixConditionParams(c.Child, prefix)
	}
	if c.Expr != nil && c.Expr.Param != "" && !strings.Contains(c.Expr.Param, ".") {
		c.Expr.Param = prefix + "." + c.Expr.Param
	}
}

func lowerParams(params []Param) ([]ir.Param, error) {
	out := make([]ir.Param, 0, len(params))
	for _, p := range params {
		ip, err := lowerParam(p)
		if err != nil {
			return nil, err
		}
		out = append(out, ip)
	}
	return out, nil
}

func lowerParam(p Param) (ir.Param, error) {
	ip := ir.Param{Name: p.Name(), Required: p.IsRequired(), Optional: p.IsOptional()}
	if p.HasDefault() {
		ip.Default = p.GetDefault()
	}
	switch t := p.(type) {
	case *StringParam:
		ip.Type = "string"
		if enums := t.GetEnumValues(); len(enums) > 0 {
			ip.Enum = enums
			ip.Type = "enum"
		}
		if t.GetPattern() != "" {
			ip.Pattern = t.GetPattern()
		}
	case *IntParam:
		ip.Type = "int"
		if m := t.GetMin(); m != nil {
			f := float64(*m)
			ip.Min = &f
		}
		if m := t.GetMax(); m != nil {
			f := float64(*m)
			ip.Max = &f
		}
	case *BoolParam:
		ip.Type = "bool"
	case *FloatParam:
		ip.Type = "number"
		if m := t.GetMin(); m != nil {
			ip.Min = m
		}
		if m := t.GetMax(); m != nil {
			ip.Max = m
		}
	case *ArrayParam:
		ip.Type = "array"
	case *MapParam:
		ip.Type = "object"
		ip.Closed = t.IsClosed()
		for _, f := range t.GetFields() {
			fp, err := lowerParam(f)
			if err != nil {
				return ir.Param{}, err
			}
			ip.Fields = append(ip.Fields, fp)
		}
	case *StringKeyMapParam, *DynamicMapParam:
		ip.Type = "object"
	case *StructParam:
		ip.Type = "object"
		for _, f := range t.GetFields() {
			fp, err := lowerStructField(f)
			if err != nil {
				return ir.Param{}, err
			}
			ip.Fields = append(ip.Fields, fp)
		}
	case *EnumParam:
		ip.Type = "enum"
		ip.Enum = t.GetValues()
	case *OpenStructParam:
		ip.Type = "object"
	default:
		return ir.Param{}, fmt.Errorf("ToDefkit: unsupported param type %T for %q", p, p.Name())
	}
	return ip, nil
}

func lowerStructField(f *StructField) (ir.Param, error) {
	ip := ir.Param{Name: f.Name(), Required: f.IsRequired(), Optional: f.IsOptional()}
	if f.HasDefault() {
		ip.Default = f.GetDefault()
	}
	switch f.FieldType() {
	case ParamTypeString:
		ip.Type = "string"
	case ParamTypeInt:
		ip.Type = "int"
	case ParamTypeBool:
		ip.Type = "bool"
	case ParamTypeFloat:
		ip.Type = "number"
	case ParamTypeArray:
		ip.Type = "array"
	case ParamTypeMap, ParamTypeStruct:
		ip.Type = "object"
	case ParamTypeEnum:
		ip.Type = "enum"
		ip.Enum = f.GetEnumValues()
	default:
		ip.Type = string(f.FieldType())
	}
	if enums := f.GetEnumValues(); len(enums) > 0 {
		ip.Enum = enums
	}
	return ip, nil
}

func lowerValue(v Value) (ir.Expr, error) {
	if v == nil {
		return ir.Expr{}, fmt.Errorf("nil value")
	}
	switch t := v.(type) {
	case *Literal:
		return ir.Expr{Lit: t.Val()}, nil
	case *ContextRef:
		path := strings.TrimPrefix(t.Path(), "context.")
		return ir.Expr{Context: path}, nil
	case *InputRef:
		return ir.Expr{Input: t.Path()}, nil
	case *PlusExpr:
		parts := make([]ir.Expr, 0, len(t.Parts()))
		for _, p := range t.Parts() {
			ex, err := lowerValue(p)
			if err != nil {
				return ir.Expr{}, err
			}
			parts = append(parts, ex)
		}
		return ir.Expr{Plus: parts}, nil
	case *InterpolatedString:
		parts := make([]ir.Expr, 0, len(t.Parts()))
		for _, part := range t.Parts() {
			ex, err := lowerValue(part)
			if err != nil {
				return ir.Expr{}, err
			}
			parts = append(parts, ex)
		}
		return ir.Expr{Concat: parts}, nil
	case *Ref:
		path := t.Path()
		if path == "parameter" {
			return ir.Expr{}, fmt.Errorf("whole parameter object refs are not supported")
		}
		if strings.HasPrefix(path, "parameter.") {
			return ir.Expr{Param: strings.TrimPrefix(path, "parameter.")}, nil
		}
		if strings.HasPrefix(path, "context.") {
			return ir.Expr{Context: strings.TrimPrefix(path, "context.")}, nil
		}
		// helper ref e.g. claimName or claimName.result
		name := strings.TrimSuffix(path, ".result")
		return ir.Expr{HelperRef: name}, nil
	case *LocalFieldRef:
		return ir.Expr{Param: t.Name()}, nil
	case Param:
		return ir.Expr{Param: t.Name()}, nil
	default:
		if p, ok := v.(Param); ok {
			return ir.Expr{Param: p.Name()}, nil
		}
		return ir.Expr{}, fmt.Errorf("ToDefkit: unsupported value type %T", v)
	}
}

func lowerCondition(c Condition) (*ir.Condition, error) {
	if c == nil {
		return nil, fmt.Errorf("nil condition")
	}
	switch t := c.(type) {
	case *IsSetCondition:
		return &ir.Condition{Kind: "isset", Param: t.ParamName()}, nil
	case *ParamCompareCondition:
		kind := "eq"
		switch t.Op() {
		case "==":
			kind = "eq"
		case "!=":
			kind = "ne"
		case ">":
			kind = "gt"
		case ">=":
			kind = "gte"
		case "<":
			kind = "lt"
		case "<=":
			kind = "lte"
		}
		return &ir.Condition{Kind: kind, Param: t.ParamName(), Value: t.CompareValue()}, nil
	case *Comparison:
		leftParam := ""
		if p, ok := t.Left().(Param); ok {
			leftParam = p.Name()
		} else if lf, ok := t.Left().(*LocalFieldRef); ok {
			leftParam = lf.Name()
		} else if r, ok := t.Left().(*Ref); ok && strings.HasPrefix(r.Path(), "parameter.") {
			leftParam = strings.TrimPrefix(r.Path(), "parameter.")
		} else {
			return nil, fmt.Errorf("unsupported comparison left %T", t.Left())
		}
		var right any
		if lit, ok := t.Right().(*Literal); ok {
			right = lit.Val()
		} else if p, ok := t.Right().(Param); ok {
			right = p // won't compare well; store name
			_ = right
			return nil, fmt.Errorf("param-param comparison not supported")
		} else {
			return nil, fmt.Errorf("unsupported comparison right %T", t.Right())
		}
		kind := "eq"
		switch t.Op() {
		case OpEq:
			kind = "eq"
		case OpNe:
			kind = "ne"
		case OpGt:
			kind = "gt"
		case OpGe:
			kind = "gte"
		case OpLt:
			kind = "lt"
		case OpLe:
			kind = "lte"
		}
		return &ir.Condition{Kind: kind, Param: leftParam, Value: right}, nil
	case *LogicalExpr:
		children := make([]ir.Condition, 0, len(t.Conditions()))
		for _, ch := range t.Conditions() {
			lc, err := lowerCondition(ch)
			if err != nil {
				return nil, err
			}
			children = append(children, *lc)
		}
		kind := "and"
		if t.Op() == OpOr {
			kind = "or"
		}
		return &ir.Condition{Kind: kind, Children: children}, nil
	case *NotExpr:
		child, err := lowerCondition(t.Cond())
		if err != nil {
			return nil, err
		}
		return &ir.Condition{Kind: "not", Child: child}, nil
	case *PathExistsCondition:
		return &ir.Condition{Kind: "pathexists", Path: t.Path()}, nil
	case *RegexMatchCondition:
		ex, err := lowerValue(t.Source())
		if err != nil {
			return nil, err
		}
		param := ex.Param
		return &ir.Condition{Kind: "matches", Param: param, Pattern: t.Pattern(), Expr: &ex}, nil
	case *LenCondition:
		n := t.Length()
		return &ir.Condition{Kind: "len", Param: t.ParamName(), Op: t.Op(), Length: &n}, nil
	case *LenValueCondition:
		ex, err := lowerValue(t.Source())
		if err != nil {
			return nil, err
		}
		n := t.Length()
		return &ir.Condition{Kind: "len", Op: t.Op(), Length: &n, Expr: &ex}, nil
	default:
		// LenOfExpr.Gt returns LenValueCondition - handled
		// Try TruthyCondition as isset-ish
		name := fmt.Sprintf("%T", c)
		if strings.Contains(name, "CUEExpr") {
			return nil, fmt.Errorf("CUEExpr conditions are not supported in ToDefkit")
		}
		return nil, fmt.Errorf("ToDefkit: unsupported condition %T", c)
	}
}
