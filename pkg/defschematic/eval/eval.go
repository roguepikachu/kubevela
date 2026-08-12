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

package eval

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/oam-dev/kubevela/pkg/defschematic/ir"
)

// Env holds runtime bindings for expression evaluation.
type Env struct {
	Params  map[string]interface{}
	Context map[string]interface{}
	Inputs  map[string]interface{}
	Helpers map[string]interface{}
}

// EvalExpr evaluates an allowlisted expression.
func EvalExpr(e ir.Expr, env *Env) (interface{}, error) {
	switch {
	case e.Lit != nil:
		return e.Lit, nil
	case e.Param != "":
		v, ok := lookupParam(env.Params, e.Param)
		if !ok {
			return nil, fmt.Errorf("param %q not set", e.Param)
		}
		return v, nil
	case e.Context != "":
		return lookupPath(env.Context, e.Context)
	case e.Input != "":
		return lookupPath(env.Inputs, e.Input)
	case e.HelperRef != "":
		if env.Helpers == nil {
			return nil, fmt.Errorf("helper %q not set", e.HelperRef)
		}
		v, ok := env.Helpers[e.HelperRef]
		if !ok {
			return nil, fmt.Errorf("helper %q not set", e.HelperRef)
		}
		return v, nil
	case e.StatusField != "":
		out, _ := env.Context["output"].(map[string]interface{})
		if out == nil {
			return "", nil
		}
		v, err := lookupPath(out, e.StatusField)
		if err != nil {
			return "", nil
		}
		return v, nil
	case e.Template != "":
		return expandTemplate(e.Template, env)
	case len(e.Plus) > 0:
		var b strings.Builder
		for _, part := range e.Plus {
			v, err := EvalExpr(part, env)
			if err != nil {
				return nil, err
			}
			b.WriteString(fmt.Sprint(v))
		}
		return b.String(), nil
	case len(e.Concat) > 0:
		var b strings.Builder
		for _, part := range e.Concat {
			v, err := EvalExpr(part, env)
			if err != nil {
				return nil, err
			}
			b.WriteString(fmt.Sprint(v))
		}
		return b.String(), nil
	case e.Object != nil:
		out := map[string]interface{}{}
		for k, child := range e.Object {
			v, err := EvalExpr(child, env)
			if err != nil {
				return nil, err
			}
			out[k] = v
		}
		return out, nil
	case e.List != nil:
		out := make([]interface{}, 0, len(e.List))
		for _, child := range e.List {
			v, err := EvalExpr(child, env)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	default:
		return nil, nil
	}
}

// EvalCondition evaluates a first-class condition.
func EvalCondition(c *ir.Condition, env *Env) (bool, error) {
	if c == nil {
		return true, nil
	}
	switch c.Kind {
	case "isset":
		_, ok := lookupParam(env.Params, c.Param)
		return ok, nil
	case "eq", "ne", "gt", "gte", "lt", "lte":
		left, ok := lookupParam(env.Params, c.Param)
		if !ok {
			return false, nil
		}
		cmp := compareValues(left, c.Value)
		switch c.Kind {
		case "eq":
			return cmp == 0, nil
		case "ne":
			return cmp != 0, nil
		case "gt":
			return cmp > 0, nil
		case "gte":
			return cmp >= 0, nil
		case "lt":
			return cmp < 0, nil
		case "lte":
			return cmp <= 0, nil
		}
	case "and":
		for i := range c.Children {
			ok, err := EvalCondition(&c.Children[i], env)
			if err != nil || !ok {
				return ok, err
			}
		}
		return true, nil
	case "or":
		for i := range c.Children {
			ok, err := EvalCondition(&c.Children[i], env)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil
	case "not":
		if c.Child == nil {
			return false, fmt.Errorf("not missing child")
		}
		ok, err := EvalCondition(c.Child, env)
		return !ok, err
	case "pathexists":
		path := strings.TrimPrefix(c.Path, "parameter.")
		if path == "" {
			path = c.Param
		}
		_, ok := lookupParam(env.Params, path)
		return ok, nil
	case "matches":
		var left interface{}
		var ok bool
		if c.Expr != nil {
			var err error
			left, err = EvalExpr(*c.Expr, env)
			if err != nil {
				return false, err
			}
			ok = true
		} else {
			left, ok = lookupParam(env.Params, c.Param)
		}
		if !ok {
			return false, nil
		}
		re, err := regexp.Compile(c.Pattern)
		if err != nil {
			return false, err
		}
		return re.MatchString(fmt.Sprint(left)), nil
	case "len":
		var v interface{}
		var err error
		if c.Expr != nil {
			v, err = EvalExpr(*c.Expr, env)
			if err != nil {
				return false, err
			}
		} else if c.Param != "" {
			v, _ = lookupParam(env.Params, c.Param)
		}
		n := valueLen(v)
		target := 0
		if c.Length != nil {
			target = *c.Length
		} else if c.Value != nil {
			if f, ok := asFloat(c.Value); ok {
				target = int(f)
			}
		}
		cmp := n - target
		switch c.Op {
		case ">", "gt":
			return cmp > 0, nil
		case ">=", "gte":
			return cmp >= 0, nil
		case "<", "lt":
			return cmp < 0, nil
		case "<=", "lte":
			return cmp <= 0, nil
		case "==", "eq", "":
			return cmp == 0, nil
		default:
			return false, fmt.Errorf("unsupported len op %q", c.Op)
		}
	default:
		return false, fmt.Errorf("unsupported condition kind %q", c.Kind)
	}
	return false, nil
}

func bindHelpers(def *ir.Definition, env *Env) error {
	env.Helpers = map[string]interface{}{}
	for _, h := range def.Helpers {
		switch h.Kind {
		case "claimName":
			if h.ClaimName == nil {
				return fmt.Errorf("helper %q missing claimName", h.Name)
			}
			maxLen := h.ClaimName.MaxLen
			if maxLen <= 0 {
				maxLen = 63
			}
			var b strings.Builder
			for _, p := range h.ClaimName.Parts {
				v, err := EvalExpr(p, env)
				if err != nil {
					return err
				}
				b.WriteString(fmt.Sprint(v))
			}
			name := b.String()
			if len([]rune(name)) > maxLen {
				runes := []rune(name)
				prefix := string(runes[:maxLen-6])
				sum := md5.Sum([]byte(name))
				suffix := hex.EncodeToString(sum[:])[:5]
				name = prefix + "-" + suffix
			}
			env.Helpers[h.Name] = name
		default:
			return fmt.Errorf("unsupported helper kind %q", h.Kind)
		}
	}
	return nil
}

func expandTemplate(tmpl string, env *Env) (string, error) {
	out := tmpl
	for k, v := range env.Params {
		out = strings.ReplaceAll(out, "{param."+k+"}", fmt.Sprint(v))
		out = strings.ReplaceAll(out, "{"+k+"}", fmt.Sprint(v))
	}
	var walk func(prefix, placeholder string, m map[string]interface{})
	walk = func(prefix, placeholder string, m map[string]interface{}) {
		for k, v := range m {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			if child, ok := v.(map[string]interface{}); ok {
				walk(path, placeholder, child)
				continue
			}
			out = strings.ReplaceAll(out, "{"+placeholder+"."+path+"}", fmt.Sprint(v))
		}
	}
	walk("", "context", env.Context)
	walk("", "input", env.Inputs)
	return out, nil
}

func lookupPath(root map[string]interface{}, path string) (interface{}, error) {
	if root == nil {
		return nil, fmt.Errorf("nil root for path %q", path)
	}
	parts := strings.Split(path, ".")
	var cur interface{} = root
	for _, p := range parts {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("path %q not found (non-object at %q)", path, p)
		}
		cur, ok = m[p]
		if !ok {
			return nil, fmt.Errorf("path %q not found", path)
		}
	}
	return cur, nil
}

func lookupParam(params map[string]interface{}, path string) (interface{}, bool) {
	path = strings.TrimPrefix(path, "parameter.")
	if path == "" || params == nil {
		return nil, false
	}
	parts := strings.Split(path, ".")
	var cur interface{} = params
	for _, p := range parts {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func truthy(v interface{}) bool {
	if v == nil {
		return false
	}
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	default:
		return true
	}
}

func compareValues(a, b interface{}) int {
	af, aok := asFloat(a)
	bf, bok := asFloat(b)
	if aok && bok {
		switch {
		case af < bf:
			return -1
		case af > bf:
			return 1
		default:
			return 0
		}
	}
	as, bs := fmt.Sprint(a), fmt.Sprint(b)
	switch {
	case as < bs:
		return -1
	case as > bs:
		return 1
	default:
		return 0
	}
}

func asFloat(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func valueLen(v interface{}) int {
	switch t := v.(type) {
	case string:
		return len([]rune(t))
	case []interface{}:
		return len(t)
	case map[string]interface{}:
		return len(t)
	default:
		return len([]rune(fmt.Sprint(v)))
	}
}

// RenderResource builds an unstructured object from an IR resource.
func RenderResource(r *ir.Resource, env *Env) (*unstructured.Unstructured, error) {
	if r == nil {
		return nil, fmt.Errorf("nil resource")
	}
	obj := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": r.APIVersion,
		"kind":       r.Kind,
		"metadata":   map[string]interface{}{},
		"spec":       map[string]interface{}{},
	}}
	for _, f := range r.Fields {
		if err := applyFieldOp(obj.Object, f, env); err != nil {
			return nil, err
		}
	}
	return obj, nil
}

func applyFieldOp(root map[string]interface{}, f ir.FieldOp, env *Env) error {
	if f.Condition != nil {
		ok, err := EvalCondition(f.Condition, env)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	if len(f.StructFields) > 0 {
		nested := map[string]interface{}{}
		for _, child := range f.StructFields {
			if err := applyFieldOp(nested, child, env); err != nil {
				return err
			}
		}
		return setField(root, f.Path, nested)
	}
	if f.Value == nil {
		return nil
	}
	v, err := EvalExpr(*f.Value, env)
	if err != nil {
		return err
	}
	if f.Spread {
		m, ok := v.(map[string]interface{})
		if !ok {
			return fmt.Errorf("spread value at %q is not an object", f.Path)
		}
		cur, _ := lookupPath(root, f.Path)
		dst, _ := cur.(map[string]interface{})
		if dst == nil {
			dst = map[string]interface{}{}
			if err := setField(root, f.Path, dst); err != nil {
				return err
			}
		}
		for k, vv := range m {
			dst[k] = vv
		}
		return nil
	}
	return setField(root, f.Path, v)
}

func setField(root map[string]interface{}, path string, value interface{}) error {
	parts := strings.Split(path, ".")
	cur := root
	for i, p := range parts {
		isLast := i == len(parts)-1
		base, key, arrIdx, kind := parsePathSegment(p)
		switch kind {
		case pathSegMapKey:
			next, ok := cur[base].(map[string]interface{})
			if !ok {
				next = map[string]interface{}{}
				cur[base] = next
			}
			if isLast {
				next[key] = value
				return nil
			}
			inner, ok := next[key].(map[string]interface{})
			if !ok {
				inner = map[string]interface{}{}
				next[key] = inner
			}
			cur = inner
		case pathSegArray:
			arr, _ := cur[base].([]interface{})
			for len(arr) <= arrIdx {
				arr = append(arr, map[string]interface{}{})
			}
			if isLast {
				arr[arrIdx] = value
				cur[base] = arr
				return nil
			}
			elem, ok := arr[arrIdx].(map[string]interface{})
			if !ok {
				elem = map[string]interface{}{}
				arr[arrIdx] = elem
			}
			cur[base] = arr
			cur = elem
		default:
			if isLast {
				cur[base] = value
				return nil
			}
			next, ok := cur[base].(map[string]interface{})
			if !ok {
				next = map[string]interface{}{}
				cur[base] = next
			}
			cur = next
		}
	}
	return nil
}

const (
	pathSegPlain = iota
	pathSegArray
	pathSegMapKey
)

func parsePathSegment(p string) (base, key string, arrIdx int, kind int) {
	bracket := strings.Index(p, "[")
	if bracket < 0 || !strings.HasSuffix(p, "]") {
		return p, "", -1, pathSegPlain
	}
	base = p[:bracket]
	inner := p[bracket+1 : len(p)-1]
	if n, err := strconv.Atoi(inner); err == nil {
		return base, "", n, pathSegArray
	}
	return base, inner, -1, pathSegMapKey
}

// ApplyPatches mutates objects according to IR patches.
func ApplyPatches(patches []ir.Patch, output *unstructured.Unstructured, outputs map[string]*unstructured.Unstructured, env *Env) error {
	for _, p := range patches {
		if p.Condition != nil {
			ok, err := EvalCondition(p.Condition, env)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
		}
		var target map[string]interface{}
		switch {
		case p.Target == "output" || p.Target == "":
			if output == nil {
				return fmt.Errorf("patch target output is nil")
			}
			target = output.Object
		case strings.HasPrefix(p.Target, "outputs."):
			name := strings.TrimPrefix(p.Target, "outputs.")
			o, ok := outputs[name]
			if !ok || o == nil {
				return fmt.Errorf("patch target %q not found", p.Target)
			}
			target = o.Object
		default:
			return fmt.Errorf("unsupported patch target %q", p.Target)
		}
		for _, f := range p.Fields {
			if f.Condition != nil {
				ok, err := EvalCondition(f.Condition, env)
				if err != nil {
					return err
				}
				if !ok {
					continue
				}
			}
			v, err := EvalExpr(f.Value, env)
			if err != nil {
				return err
			}
			if err := setField(target, f.Path, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// Result is the evaluation result for a component/policy template.
type Result struct {
	Output  *unstructured.Unstructured
	Outputs map[string]*unstructured.Unstructured
}

func prepareEnv(def *ir.Definition, params, ctxMap map[string]interface{}) (*Env, error) {
	validated, err := ir.ValidateParamsFull(def.Params, def.When, def.Validators, params, &ir.ValidateEnv{Params: params, Context: ctxMap})
	if err != nil {
		return nil, err
	}
	env := &Env{Params: validated, Context: ctxMap, Inputs: map[string]interface{}{}}
	if err := bindHelpers(def, env); err != nil {
		return nil, err
	}
	return env, nil
}

// EvalDefinition evaluates a definition with params and context.
func EvalDefinition(def *ir.Definition, params map[string]interface{}, ctxMap map[string]interface{}) (*Result, error) {
	env, err := prepareEnv(def, params, ctxMap)
	if err != nil {
		return nil, err
	}

	switch def.Kind {
	case ir.KindWorkflowStep:
		return nil, fmt.Errorf("use EvalWorkflowStep for workflow steps")
	case ir.KindTrait:
		return &Result{Outputs: map[string]*unstructured.Unstructured{}}, nil
	}

	res := &Result{Outputs: map[string]*unstructured.Unstructured{}}
	tmpl := def.Template
	if def.Kind == ir.KindPolicy && def.Policy != nil && def.Policy.Output != nil {
		tmpl = &ir.Template{Output: def.Policy.Output}
	}
	if tmpl == nil || tmpl.Output == nil {
		return nil, fmt.Errorf("definition %q has no template.output", def.Name)
	}
	out, err := RenderResource(tmpl.Output, env)
	if err != nil {
		return nil, err
	}
	res.Output = out
	for name, r := range tmpl.Outputs {
		o, err := RenderResource(r, env)
		if err != nil {
			return nil, fmt.Errorf("outputs.%s: %w", name, err)
		}
		res.Outputs[name] = o
	}
	if len(tmpl.Patches) > 0 {
		if err := ApplyPatches(tmpl.Patches, res.Output, res.Outputs, env); err != nil {
			return nil, err
		}
	}
	return res, nil
}

// EvalTrait applies trait patches onto an existing component result.
func EvalTrait(def *ir.Definition, params map[string]interface{}, ctxMap map[string]interface{}, base *Result) (*Result, error) {
	env, err := prepareEnv(def, params, ctxMap)
	if err != nil {
		return nil, err
	}
	if base == nil {
		base = &Result{Outputs: map[string]*unstructured.Unstructured{}}
	}
	out := &Result{
		Output:  base.Output,
		Outputs: map[string]*unstructured.Unstructured{},
	}
	for k, v := range base.Outputs {
		out.Outputs[k] = v
	}
	tmpl := def.Template
	if tmpl == nil {
		return out, nil
	}
	for name, r := range tmpl.Outputs {
		o, err := RenderResource(r, env)
		if err != nil {
			return nil, err
		}
		out.Outputs[name] = o
	}
	if err := ApplyPatches(tmpl.Patches, out.Output, out.Outputs, env); err != nil {
		return nil, err
	}
	return out, nil
}

// WorkflowStepResult is native workflow step evaluation output.
type WorkflowStepResult struct {
	Outputs map[string]interface{}
	Message string
}

// EvalWorkflowStep evaluates workflow step logic without CUE.
func EvalWorkflowStep(def *ir.Definition, params, inputs, ctxMap map[string]interface{}) (*WorkflowStepResult, error) {
	env, err := prepareEnv(def, params, ctxMap)
	if err != nil {
		return nil, err
	}
	env.Inputs = inputs
	if def.Workflow == nil {
		return nil, fmt.Errorf("workflow step %q missing workflow body", def.Name)
	}
	res := &WorkflowStepResult{Outputs: map[string]interface{}{}}
	for k, expr := range def.Workflow.Outputs {
		v, err := EvalExpr(expr, env)
		if err != nil {
			return nil, err
		}
		res.Outputs[k] = v
	}
	if def.Workflow.Message.Lit != nil || def.Workflow.Message.Param != "" || def.Workflow.Message.Template != "" || def.Workflow.Message.Context != "" || len(def.Workflow.Message.Concat) > 0 || len(def.Workflow.Message.Plus) > 0 {
		msg, err := EvalExpr(def.Workflow.Message, env)
		if err != nil {
			return nil, err
		}
		res.Message = fmt.Sprint(msg)
	}
	return res, nil
}

// EvalHealth evaluates native health/status from a definition and template context.
func EvalHealth(def *ir.Definition, templateContext map[string]interface{}) (healthy bool, message string, err error) {
	if def.Health == nil {
		return true, "defkit-poc: no health spec", nil
	}
	env := &Env{Params: map[string]interface{}{}, Context: templateContext, Helpers: map[string]interface{}{}}
	if p, ok := templateContext["parameter"].(map[string]interface{}); ok {
		env.Params = p
	}
	switch def.Health.Type {
	case ir.HealthCrossplaneClaim:
		healthy = crossplaneReadySynced(templateContext)
	case ir.HealthStatusExists:
		out, _ := templateContext["output"].(map[string]interface{})
		if out != nil {
			_, healthy = out["status"]
		}
	default:
		return false, "", fmt.Errorf("unsupported health type %q", def.Health.Type)
	}
	if def.Status != nil {
		var msgExpr ir.Expr
		if healthy {
			msgExpr = def.Status.HealthyMessage
		} else {
			msgExpr = def.Status.UnhealthyMessage
		}
		if msgExpr.Lit != nil || msgExpr.Param != "" || len(msgExpr.Concat) > 0 || len(msgExpr.Plus) > 0 || msgExpr.StatusField != "" || msgExpr.Context != "" || msgExpr.Template != "" {
			v, err := EvalExpr(msgExpr, env)
			if err != nil {
				return healthy, "", err
			}
			message = fmt.Sprint(v)
		}
	}
	if message == "" {
		if healthy {
			message = "healthy"
		} else {
			message = "unhealthy"
		}
	}
	return healthy, message, nil
}

func crossplaneReadySynced(templateContext map[string]interface{}) bool {
	out, _ := templateContext["output"].(map[string]interface{})
	if out == nil {
		return false
	}
	status, _ := out["status"].(map[string]interface{})
	if status == nil {
		return false
	}
	conds, _ := status["conditions"].([]interface{})
	ready, synced := false, false
	for _, c := range conds {
		m, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		t := fmt.Sprint(m["type"])
		s := fmt.Sprint(m["status"])
		if t == "Ready" && s == "True" {
			ready = true
		}
		if t == "Synced" && s == "True" {
			synced = true
		}
	}
	return ready && synced
}
