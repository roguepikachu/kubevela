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

// Package ir defines the defkit schematic IR.
// The schematic document is data: it is authored at compile time and evaluated by allowlisted ops.
package ir

import (
	"encoding/json"
	"fmt"
)

const (
	// APIVersion identifies the defkit schematic schema version stored in Definition CRs.
	APIVersion       = "defkit.oam.dev/v1alpha1"
	KindComponent    = "Component"
	KindTrait        = "Trait"
	KindPolicy       = "Policy"
	KindWorkflowStep = "WorkflowStep"

	// HealthCrossplaneClaim marks Ready && Synced condition health.
	HealthCrossplaneClaim = "crossplaneClaim"
	// HealthStatusExists is healthy when context.output.status is present.
	HealthStatusExists = "statusExists"
)

// Definition is the top-level document stored in schematic.defkit.template (JSON).
type Definition struct {
	APIVersion string                 `json:"apiVersion"`
	Kind       string                 `json:"kind"`
	Name       string                 `json:"name"`
	Params     []Param                `json:"params,omitempty"`
	When       []ConditionalParamBlock `json:"when,omitempty"`
	Validators []Validator            `json:"validators,omitempty"`
	Helpers    []Helper               `json:"helpers,omitempty"`
	Health     *HealthSpec            `json:"health,omitempty"`
	Status     *StatusSpec            `json:"status,omitempty"`
	Template   *Template              `json:"template,omitempty"`
	Workflow   *Workflow              `json:"workflow,omitempty"`
	Policy     *Policy                `json:"policy,omitempty"`
}

// Param declares a typed parameter with optional default and validation.
type Param struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"` // string, int, bool, number, object, array, enum
	Required bool    `json:"required,omitempty"`
	Optional bool    `json:"optional,omitempty"`
	Default  interface{} `json:"default,omitempty"`
	Enum     []string `json:"enum,omitempty"`
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	Pattern  string   `json:"pattern,omitempty"`
	Fields   []Param  `json:"fields,omitempty"` // nested object fields
	Items    *Param   `json:"items,omitempty"`  // array element schema
	Closed   bool     `json:"closed,omitempty"`
}

// ConditionalParamBlock adds params when a condition holds.
type ConditionalParamBlock struct {
	When       *Condition `json:"when"`
	Params     []Param    `json:"params,omitempty"`
	Validators []Validator `json:"validators,omitempty"`
}

// Validator is an eval-time parameter check.
type Validator struct {
	Name     string     `json:"name,omitempty"`
	Message  string     `json:"message,omitempty"`
	OnlyWhen *Condition `json:"onlyWhen,omitempty"`
	FailWhen *Condition `json:"failWhen"`
}

// Helper is a named precomputed value (e.g. claimName).
type Helper struct {
	Name    string          `json:"name"`
	Kind    string          `json:"kind"` // claimName
	ClaimName *ClaimNameHelper `json:"claimName,omitempty"`
}

// ClaimNameHelper builds a tenant-prefixed name with optional md5 truncate.
type ClaimNameHelper struct {
	Parts  []Expr `json:"parts"`
	MaxLen int    `json:"maxLen,omitempty"` // default 63
}

// HealthSpec describes native health evaluation.
type HealthSpec struct {
	Type string `json:"type"` // crossplaneClaim
}

// StatusSpec describes custom status message / details.
type StatusSpec struct {
	HealthyMessage   Expr            `json:"healthyMessage,omitempty"`
	UnhealthyMessage Expr            `json:"unhealthyMessage,omitempty"`
	Details          map[string]Expr `json:"details,omitempty"`
}

// Template describes component/trait resource synthesis.
type Template struct {
	Output  *Resource            `json:"output,omitempty"`
	Outputs map[string]*Resource `json:"outputs,omitempty"`
	Patches []Patch              `json:"patches,omitempty"`
}

// Resource is a Kubernetes object skeleton with field setters.
type Resource struct {
	APIVersion string     `json:"apiVersion"`
	Kind       string     `json:"kind"`
	Fields     []FieldOp  `json:"fields,omitempty"`
}

// FieldOp is a Set, SetIf, SpreadIf, or ConditionalStruct.
type FieldOp struct {
	Path      string     `json:"path,omitempty"`
	Value     *Expr      `json:"value,omitempty"`
	Condition *Condition `json:"condition,omitempty"`
	// Spread merges a map value into Path when Condition holds.
	Spread bool `json:"spread,omitempty"`
	// StructFields materializes a nested object at Path when Condition holds.
	StructFields []FieldOp `json:"structFields,omitempty"`
}

// FieldSet is kept for backward-compatible patches (Set/SetIf only).
type FieldSet struct {
	Path      string     `json:"path"`
	Value     Expr       `json:"value"`
	Condition *Condition `json:"condition,omitempty"`
}

// Patch mutates an existing resource (trait post-render).
type Patch struct {
	Target    string     `json:"target"` // "output" or "outputs.<name>"
	Fields    []FieldSet `json:"fields,omitempty"`
	Condition *Condition `json:"condition,omitempty"`
}

// Workflow describes a workflow step body for native evaluation.
type Workflow struct {
	Outputs map[string]Expr `json:"outputs,omitempty"`
	Message Expr            `json:"message,omitempty"`
}

// Policy describes simple application-level policy data (override-style).
type Policy struct {
	Output *Resource       `json:"output,omitempty"`
	Data   map[string]Expr `json:"data,omitempty"`
}

// Expr is a declarative expression node (allowlisted).
type Expr struct {
	Lit         interface{}      `json:"lit,omitempty"`
	Param       string           `json:"param,omitempty"` // dotted path under parameter
	Context     string           `json:"context,omitempty"`
	Input       string           `json:"input,omitempty"`
	Concat      []Expr           `json:"concat,omitempty"`
	Plus        []Expr           `json:"plus,omitempty"`
	Template    string           `json:"template,omitempty"`
	Object      map[string]Expr  `json:"object,omitempty"`
	List        []Expr           `json:"list,omitempty"`
	HelperRef   string           `json:"helperRef,omitempty"`   // e.g. claimName
	StatusField string           `json:"statusField,omitempty"` // path under context.output
}

// Condition is a first-class boolean expression.
type Condition struct {
	Kind     string       `json:"kind"` // isset, eq, ne, gt, gte, lt, lte, and, or, not, pathexists, matches, len
	Param    string       `json:"param,omitempty"`
	Path     string       `json:"path,omitempty"` // for pathexists (parameter. or context.)
	Op       string       `json:"op,omitempty"`   // for compare/len
	Value    interface{}  `json:"value,omitempty"`
	Expr     *Expr        `json:"expr,omitempty"` // left side for len/matches
	Pattern  string       `json:"pattern,omitempty"`
	Length   *int         `json:"length,omitempty"`
	Children []Condition  `json:"children,omitempty"` // and/or
	Child    *Condition   `json:"child,omitempty"`    // not
}

// ParseJSON decodes a defkit schematic definition from JSON bytes.
func ParseJSON(b []byte) (*Definition, error) {
	var d Definition
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("parse defkit schematic: %w", err)
	}
	if d.APIVersion == "" {
		d.APIVersion = APIVersion
	}
	// Migrate legacy FieldSet-shaped resource fields if present via raw decode fallback.
	d.normalizeLegacyFields()
	return &d, nil
}

// normalizeLegacyFields converts old {path,value,condition:Expr} shapes if unmarshaled into FieldOp.
func (d *Definition) normalizeLegacyFields() {
	// no-op: FieldOp.Condition is Condition; old tests rebuilt via ToDefkit
}

// ToJSON encodes a defkit schematic definition.
func (d *Definition) ToJSON() ([]byte, error) {
	if d.APIVersion == "" {
		d.APIVersion = APIVersion
	}
	return json.Marshal(d)
}

// MustJSON returns JSON or panics (for tests/examples).
func (d *Definition) MustJSON() string {
	b, err := d.ToJSON()
	if err != nil {
		panic(err)
	}
	return string(b)
}
