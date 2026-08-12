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

// Package cuebridge emits a best-effort CUE approximation of a DIR definition
// for golden parity tests and gradual migration. It is not required for native
// DIR evaluation.
package cuebridge

import (
	"fmt"
	"strings"

	"github.com/oam-dev/kubevela/pkg/defschematic/ir"
)

// ToCUE converts a component-like DIR definition into a CUE template fragment
// (parameter + output). Trait/workflow coverage is intentionally partial.
func ToCUE(def *ir.Definition) (string, error) {
	var b strings.Builder
	b.WriteString("parameter: {\n")
	for _, p := range def.Params {
		line := fmt.Sprintf("\t%s?: %s", p.Name, cueType(p.Type))
		if p.Default != nil {
			line = fmt.Sprintf("\t%s: *%s | %s", p.Name, cueLit(p.Default), cueType(p.Type))
		}
		if p.Required && p.Default == nil {
			line = fmt.Sprintf("\t%s: %s", p.Name, cueType(p.Type))
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("}\n")
	if def.Template != nil && def.Template.Output != nil {
		b.WriteString("output: {\n")
		b.WriteString(fmt.Sprintf("\tapiVersion: %q\n", def.Template.Output.APIVersion))
		b.WriteString(fmt.Sprintf("\tkind: %q\n", def.Template.Output.Kind))
		for _, f := range def.Template.Output.Fields {
			b.WriteString(fmt.Sprintf("\t// field %s (see DIR native eval for full fidelity)\n", f.Path))
		}
		b.WriteString("}\n")
	}
	return b.String(), nil
}

func cueType(t string) string {
	switch t {
	case "int", "number":
		return "int"
	case "bool":
		return "bool"
	default:
		return "string"
	}
}

func cueLit(v interface{}) string {
	switch t := v.(type) {
	case string:
		return fmt.Sprintf("%q", t)
	case bool:
		return fmt.Sprintf("%v", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}
