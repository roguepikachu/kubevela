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

package ir

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// ValidateParams applies defaults and validates parameter values against Param decls
// and active ConditionalParamBlock branches.
func ValidateParams(params []Param, values map[string]interface{}) (map[string]interface{}, error) {
	return ValidateParamsFull(params, nil, nil, values, nil)
}

// ValidateParamsFull applies base params, conditional branches, and validators.
func ValidateParamsFull(params []Param, when []ConditionalParamBlock, validators []Validator, values map[string]interface{}, env *ValidateEnv) (map[string]interface{}, error) {
	out := map[string]interface{}{}
	for k, v := range values {
		out[k] = v
	}
	// Apply base defaults first so When conditions see them.
	for _, p := range params {
		v, ok := out[p.Name]
		if (!ok || v == nil) && p.Default != nil {
			out[p.Name] = cloneDefault(p.Default)
		}
	}
	if env == nil {
		env = &ValidateEnv{Params: out}
	} else {
		env.Params = out
	}

	active := append([]Param{}, params...)
	activeValidators := append([]Validator{}, validators...)
	for _, block := range when {
		ok, err := EvalConditionStatic(block.When, env)
		if err != nil {
			return nil, err
		}
		if ok {
			active = append(active, block.Params...)
			activeValidators = append(activeValidators, block.Validators...)
			for _, p := range block.Params {
				v, has := out[p.Name]
				if (!has || v == nil) && p.Default != nil {
					out[p.Name] = cloneDefault(p.Default)
				}
			}
		}
	}
	env.Params = out

	for _, p := range active {
		v, ok := out[p.Name]
		if !ok || v == nil {
			if p.Default != nil {
				out[p.Name] = cloneDefault(p.Default)
				v = out[p.Name]
				ok = true
			} else if p.Required && !p.Optional {
				return nil, fmt.Errorf("parameter %q is required", p.Name)
			} else {
				continue
			}
		}
		if err := validateOne(p, v); err != nil {
			return nil, err
		}
	}

	for _, v := range activeValidators {
		if v.OnlyWhen != nil {
			ok, err := EvalConditionStatic(v.OnlyWhen, env)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
		}
		if v.FailWhen == nil {
			continue
		}
		fail, err := EvalConditionStatic(v.FailWhen, env)
		if err != nil {
			return nil, err
		}
		if fail {
			msg := v.Message
			if msg == "" {
				msg = v.Name
			}
			if msg == "" {
				msg = "validation failed"
			}
			return nil, fmt.Errorf("%s", msg)
		}
	}
	return out, nil
}

// ValidateEnv is a minimal env for condition checks during param validation.
type ValidateEnv struct {
	Params  map[string]interface{}
	Context map[string]interface{}
}

// EvalConditionStatic evaluates a condition against ValidateEnv (params map).
// Full eval package uses this shape for consistency during validation.
func EvalConditionStatic(c *Condition, env *ValidateEnv) (bool, error) {
	if c == nil {
		return true, nil
	}
	switch c.Kind {
	case "isset":
		v, ok := lookupParam(env.Params, c.Param)
		return ok && v != nil, nil
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
			ok, err := EvalConditionStatic(&c.Children[i], env)
			if err != nil || !ok {
				return ok, err
			}
		}
		return true, nil
	case "or":
		for i := range c.Children {
			ok, err := EvalConditionStatic(&c.Children[i], env)
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
			return false, fmt.Errorf("not condition missing child")
		}
		ok, err := EvalConditionStatic(c.Child, env)
		return !ok, err
	case "pathexists":
		path := c.Path
		if path == "" {
			path = c.Param
		}
		path = trimParameterPrefix(path)
		_, ok := lookupParam(env.Params, path)
		return ok, nil
	case "matches":
		left, ok := lookupParam(env.Params, c.Param)
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
		if c.Expr != nil && c.Expr.Plus != nil {
			// approximate: evaluate plus as concat of string parts from params/lits
			var b string
			for _, p := range c.Expr.Plus {
				if p.Lit != nil {
					b += fmt.Sprint(p.Lit)
				} else if p.Param != "" {
					pv, _ := lookupParam(env.Params, p.Param)
					b += fmt.Sprint(pv)
				}
			}
			v = b
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

func validateOne(p Param, v interface{}) error {
	if len(p.Enum) > 0 {
		s := fmt.Sprint(v)
		found := false
		for _, e := range p.Enum {
			if e == s {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("parameter %q value %v not in enum %v", p.Name, v, p.Enum)
		}
	}
	if p.Pattern != "" {
		s := fmt.Sprint(v)
		re, err := regexp.Compile(p.Pattern)
		if err != nil {
			return fmt.Errorf("parameter %q invalid pattern: %w", p.Name, err)
		}
		if !re.MatchString(s) {
			return fmt.Errorf("parameter %q value %q does not match pattern %q", p.Name, s, p.Pattern)
		}
	}
	switch p.Type {
	case "int", "number":
		n, ok := asFloat(v)
		if !ok {
			return fmt.Errorf("parameter %q expects number, got %T", p.Name, v)
		}
		if p.Min != nil && n < *p.Min {
			return fmt.Errorf("parameter %q below min %v", p.Name, *p.Min)
		}
		if p.Max != nil && n > *p.Max {
			return fmt.Errorf("parameter %q above max %v", p.Name, *p.Max)
		}
	case "string", "enum":
		if _, ok := v.(string); !ok {
			// allow non-string if enum matched via Sprint
			if len(p.Enum) == 0 {
				return fmt.Errorf("parameter %q expects string, got %T", p.Name, v)
			}
		}
	case "bool":
		if _, ok := v.(bool); !ok {
			return fmt.Errorf("parameter %q expects bool, got %T", p.Name, v)
		}
	case "object":
		m, ok := v.(map[string]interface{})
		if !ok {
			return fmt.Errorf("parameter %q expects object, got %T", p.Name, v)
		}
		for _, f := range p.Fields {
			fv, fok := m[f.Name]
			if !fok || fv == nil {
				if f.Default != nil {
					m[f.Name] = cloneDefault(f.Default)
					continue
				}
				if f.Required && !f.Optional {
					return fmt.Errorf("parameter %q.%s is required", p.Name, f.Name)
				}
				continue
			}
			if err := validateOne(f, fv); err != nil {
				return err
			}
		}
	case "array":
		// accept any slice
		switch v.(type) {
		case []interface{}, []string, []map[string]interface{}:
		default:
			return fmt.Errorf("parameter %q expects array, got %T", p.Name, v)
		}
	}
	return nil
}

func lookupParam(params map[string]interface{}, path string) (interface{}, bool) {
	path = trimParameterPrefix(path)
	if path == "" || params == nil {
		return nil, false
	}
	parts := splitPath(path)
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

func trimParameterPrefix(path string) string {
	if len(path) > 10 && path[:10] == "parameter." {
		return path[10:]
	}
	return path
}

func splitPath(path string) []string {
	var parts []string
	cur := ""
	for _, r := range path {
		if r == '.' {
			if cur != "" {
				parts = append(parts, cur)
				cur = ""
			}
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		parts = append(parts, cur)
	}
	return parts
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
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func cloneDefault(v interface{}) interface{} {
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return out
}
