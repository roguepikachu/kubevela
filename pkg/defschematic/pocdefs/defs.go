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

// Package pocdefs contains PoC definitions authored only with pkg/definition/defkit
// and emitted via ToDefkit / ToDefkitYAML (schematic.defkit).
package pocdefs

import (
	"github.com/oam-dev/kubevela/pkg/definition/defkit"
	"github.com/oam-dev/kubevela/pkg/defschematic/ir"
)

// DefkitWebservice builds a multi-resource component (Deployment + Service).
func DefkitWebservice() *defkit.ComponentDefinition {
	image := defkit.String("image").Required()
	replicas := defkit.Int("replicas").Default(1).Min(1).Max(10)
	port := defkit.Int("port").Default(80)

	return defkit.NewComponent("defkit-webservice").
		Description("PoC webservice authored with defkit ToDefkit emit").
		Workload("apps/v1", "Deployment").
		Params(image, replicas, port).
		Template(func(tpl *defkit.Template) {
			deploy := defkit.NewResource("apps/v1", "Deployment").
				Set("metadata.name", tpl.Name()).
				Set("metadata.namespace", tpl.Namespace()).
				Set("spec.replicas", replicas).
				Set("spec.selector.matchLabels.app", tpl.Name()).
				Set("spec.template.metadata.labels.app", tpl.Name()).
				Set("spec.template.spec.containers[0].name", tpl.Name()).
				Set("spec.template.spec.containers[0].image", image).
				Set("spec.template.spec.containers[0].ports[0].containerPort", port)
			tpl.Output(deploy)

			svc := defkit.NewResource("v1", "Service").
				Set("metadata.name", defkit.Interpolation(tpl.Name(), defkit.Lit("-svc"))).
				Set("metadata.namespace", tpl.Namespace()).
				Set("spec.selector.app", tpl.Name()).
				Set("spec.ports[0].port", port).
				Set("spec.ports[0].targetPort", port)
			tpl.Outputs("service", svc)
		})
}

// DefkitScaler is a trait that patches replicas on the component output.
func DefkitScaler() *defkit.TraitDefinition {
	replicas := defkit.Int("replicas").Required().Min(1).Max(20)
	return defkit.NewTrait("defkit-scaler").
		Description("PoC scaler trait via ToDefkit").
		AppliesTo("*").
		Params(replicas).
		Template(func(tpl *defkit.Template) {
			tpl.Patch().Set("spec.replicas", replicas)
		})
}

// DefkitOverridePolicy emits a ConfigMap capturing override-style configuration.
func DefkitOverridePolicy() *defkit.PolicyDefinition {
	components := defkit.String("components").Default("")
	return defkit.NewPolicy("defkit-override").
		Description("PoC override policy marker via ToDefkit").
		Params(components).
		Template(func(tpl *defkit.PolicyTemplate) {
			cm := defkit.NewResource("v1", "ConfigMap").
				Set("metadata.name", defkit.Interpolation(defkit.VelaCtx().Name(), defkit.Lit("-defkit-policy"))).
				Set("metadata.namespace", defkit.VelaCtx().Namespace()).
				Set("data.components", components).
				Set("data.engine", defkit.Lit("defkit"))
			tpl.Output(cm)
		})
}

// DefkitPassStep is a workflow step that echoes input message into outputs (unit-tested via ToDefkit).
func DefkitPassStep() *defkit.WorkflowStepDefinition {
	prefix := defkit.String("prefix").Default("defkit")
	return defkit.NewWorkflowStep("defkit-pass").
		Description("PoC workflow step body via ToDefkit").
		Params(prefix).
		Template(func(tpl *defkit.WorkflowStepTemplate) {
			tpl.Set("outputs.echo", defkit.Interpolation(prefix, defkit.Lit(":"), defkit.Input("message")))
			tpl.Set("message", defkit.Interpolation(defkit.Lit("passed "), defkit.Input("message")))
		})
}

// MustDefkitIR is a test helper that panics on ToDefkit errors.
func MustDefkitIR(d interface {
	ToDefkit() (*ir.Definition, error)
}) *ir.Definition {
	out, err := d.ToDefkit()
	if err != nil {
		panic(err)
	}
	return out
}
