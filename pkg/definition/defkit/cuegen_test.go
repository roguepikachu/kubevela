/*
Copyright 2025 The KubeVela Authors.

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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/oam-dev/kubevela/pkg/definition/defkit"
)

var _ = Describe("CUEGenerator", func() {
	var gen *defkit.CUEGenerator

	BeforeEach(func() {
		gen = defkit.NewCUEGenerator()
	})

	Describe("GenerateParameterSchema", func() {
		It("should generate CUE for string parameters", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.String("image").Required().Description("Container image"),
					defkit.String("tag").Default("latest").Description("Image tag"),
				)

			cue := gen.GenerateParameterSchema(comp)

			Expect(cue).To(ContainSubstring("// +usage=Container image"))
			Expect(cue).To(ContainSubstring("image: string"))
			Expect(cue).To(ContainSubstring("// +usage=Image tag"))
			Expect(cue).To(ContainSubstring(`tag: *"latest" | string`))
		})

		It("should generate CUE for integer parameters", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.Int("replicas").Default(1).Description("Number of replicas"),
					defkit.Int("port").Required(),
				)

			cue := gen.GenerateParameterSchema(comp)

			Expect(cue).To(ContainSubstring("replicas: *1 | int"))
			Expect(cue).To(ContainSubstring("port: int"))
		})

		It("should generate CUE for boolean parameters", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.Bool("enabled").Default(true),
					defkit.Bool("debug").Default(false),
				)

			cue := gen.GenerateParameterSchema(comp)

			Expect(cue).To(ContainSubstring("enabled: *true | bool"))
			Expect(cue).To(ContainSubstring("debug: *false | bool"))
		})

		It("should generate CUE for array parameters", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.StringList("cmd").Description("Commands"),
					defkit.List("env").Description("Environment variables"),
				)

			cue := gen.GenerateParameterSchema(comp)

			Expect(cue).To(ContainSubstring("cmd?: [...string]"))
			// List() creates an untyped array which generates [..._]
			Expect(cue).To(ContainSubstring("env?: [..._]"))
		})

		It("should generate CUE for object parameters", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.Object("config").Description("Configuration object"),
				)

			cue := gen.GenerateParameterSchema(comp)

			Expect(cue).To(ContainSubstring("config?: {...}"))
		})

		It("should generate CUE for string-key map parameters", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.StringKeyMap("labels").Description("Labels"),
				)

			cue := gen.GenerateParameterSchema(comp)

			Expect(cue).To(ContainSubstring("labels?: [string]: string"))
		})

		It("should mark required parameters without ?", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.String("required").Required(),
					defkit.String("optional"),
				)

			cue := gen.GenerateParameterSchema(comp)

			Expect(cue).To(ContainSubstring("required: string"))
			Expect(cue).To(ContainSubstring("optional?: string"))
		})

		It("should keep ? for ForceOptional parameters even with defaults", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.String("normalDefault").Default("Honor").Enum("Honor", "Ignore"),
					defkit.String("optionalDefault").Default("Honor").ForceOptional().Enum("Honor", "Ignore"),
				)

			cue := gen.GenerateParameterSchema(comp)

			// Normal default: no ? (field is always present)
			Expect(cue).To(ContainSubstring(`normalDefault: *"Honor" | "Ignore"`))
			Expect(cue).NotTo(ContainSubstring(`normalDefault?:`))

			// ForceOptional with default: has ? (field can be absent, defaults when present)
			Expect(cue).To(ContainSubstring(`optionalDefault?: *"Honor" | "Ignore"`))
		})
	})

	Describe("GenerateParameterSchema with complex types", func() {
		It("should generate CUE for struct parameters with nested fields", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.Struct("resources").Fields(
						defkit.Field("cpu", defkit.ParamTypeString).Default("100m"),
						defkit.Field("memory", defkit.ParamTypeString).Default("128Mi"),
					).Description("Resource limits"),
				)

			cue := gen.GenerateParameterSchema(comp)

			Expect(cue).To(ContainSubstring("// +usage=Resource limits"))
			Expect(cue).To(ContainSubstring("resources?:"))
			Expect(cue).To(ContainSubstring("cpu:"))
			Expect(cue).To(ContainSubstring("memory:"))
		})

		It("should generate CUE for enum parameters", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.Enum("protocol").Values("TCP", "UDP", "SCTP").Default("TCP"),
				)

			cue := gen.GenerateParameterSchema(comp)

			Expect(cue).To(ContainSubstring(`*"TCP"`))
			Expect(cue).To(ContainSubstring(`"UDP"`))
			Expect(cue).To(ContainSubstring(`"SCTP"`))
		})

		It("should generate CUE for float parameters", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.Float("ratio").Default(0.5).Description("Scale ratio"),
				)

			cue := gen.GenerateParameterSchema(comp)

			Expect(cue).To(ContainSubstring("// +usage=Scale ratio"))
			Expect(cue).To(ContainSubstring("ratio: *0.5 | float"))
		})

		It("should generate CUE for array parameters with fields", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.List("ports").WithFields(
						defkit.Int("port").Required(),
						defkit.String("name"),
						defkit.Enum("protocol").Values("TCP", "UDP").Default("TCP"),
					),
				)

			cue := gen.GenerateParameterSchema(comp)

			Expect(cue).To(ContainSubstring("ports?:"))
			Expect(cue).To(ContainSubstring("port:"))
			Expect(cue).To(ContainSubstring("name?:"))
			Expect(cue).To(ContainSubstring("protocol:"))
		})

		It("should generate CUE for nested array struct fields", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.Struct("selector").Fields(
						defkit.Field("matchExpressions", defkit.ParamTypeArray).
							Nested(defkit.Struct("matchExpression").Fields(
								defkit.Field("key", defkit.ParamTypeString).Required(),
								defkit.Field("operator", defkit.ParamTypeString),
							)),
					),
				)

			cue := gen.GenerateParameterSchema(comp)

			Expect(cue).To(ContainSubstring("matchExpressions?: [...{"))
			Expect(cue).To(ContainSubstring("key: string"))
			Expect(cue).To(ContainSubstring("operator?: string"))
		})
	})

	Describe("GenerateParameterSchema with OneOf parameters", func() {
		It("should generate discriminator field and conditional variant blocks", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.OneOf("type").
						Default("emptyDir").
						Description("Volume type").
						Variants(
							defkit.Variant("pvc").Fields(
								defkit.Field("claimName", defkit.ParamTypeString).Required(),
							),
							defkit.Variant("emptyDir").Fields(
								defkit.Field("medium", defkit.ParamTypeString).Default("").Enum("", "Memory"),
							),
						),
				)

			cue := gen.GenerateParameterSchema(comp)

			// Discriminator field with default
			Expect(cue).To(ContainSubstring(`*"emptyDir"`))
			Expect(cue).To(ContainSubstring(`"pvc"`))
			// Conditional blocks
			Expect(cue).To(ContainSubstring(`if type == "pvc"`))
			Expect(cue).To(ContainSubstring("claimName: string"))
			Expect(cue).To(ContainSubstring(`if type == "emptyDir"`))
			Expect(cue).To(ContainSubstring(`medium:`))
		})

		It("should generate OneOf inside array with shared fields", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.List("volumes").WithFields(
						defkit.String("name").Required(),
						defkit.OneOf("type").Default("emptyDir").Variants(
							defkit.Variant("pvc").Fields(
								defkit.Field("claimName", defkit.ParamTypeString).Required(),
							),
							defkit.Variant("emptyDir"),
						),
					),
				)

			cue := gen.GenerateParameterSchema(comp)

			Expect(cue).To(ContainSubstring("volumes?:"))
			Expect(cue).To(ContainSubstring("[...{"))
			Expect(cue).To(ContainSubstring("name: string"))
			Expect(cue).To(ContainSubstring(`type: *"emptyDir" | "pvc"`))
			Expect(cue).To(ContainSubstring(`if type == "pvc"`))
			Expect(cue).To(ContainSubstring("claimName: string"))
		})

		It("should omit conditional block for empty variants", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.OneOf("kind").
						Variants(
							defkit.Variant("simple"), // no fields
							defkit.Variant("complex").Fields(
								defkit.Field("config", defkit.ParamTypeString).Required(),
							),
						),
				)

			cue := gen.GenerateParameterSchema(comp)

			Expect(cue).NotTo(ContainSubstring(`if kind == "simple"`))
			Expect(cue).To(ContainSubstring(`if kind == "complex"`))
			Expect(cue).To(ContainSubstring("config: string"))
		})
	})

	Describe("GenerateFullDefinition with MapVariant", func() {
		It("should generate conditional field blocks in comprehension", func() {
			volumes := defkit.List("volumes")
			comp := defkit.NewComponent("test").
				Workload("batch/v1", "Job").
				Params(volumes).
				Template(func(tpl *defkit.Template) {
					tpl.Output(
						defkit.NewResource("batch/v1", "Job").
							Set("spec.volumes",
								defkit.Each(volumes).
									Map(defkit.FieldMap{"name": defkit.FieldRef("name")}).
									MapVariant("type", "pvc", defkit.FieldMap{
										"persistentVolumeClaim.claimName": defkit.FieldRef("claimName"),
									}).
									MapVariant("type", "emptyDir", defkit.FieldMap{
										"emptyDir.medium": defkit.FieldRef("medium"),
									}),
							),
					)
				})

			cue := gen.GenerateFullDefinition(comp)

			Expect(cue).To(ContainSubstring("for v in parameter.volumes"))
			Expect(cue).To(ContainSubstring("name: v.name"))
			Expect(cue).To(ContainSubstring(`if v.type == "pvc"`))
			Expect(cue).To(ContainSubstring("persistentVolumeClaim.claimName: v.claimName"))
			Expect(cue).To(ContainSubstring(`if v.type == "emptyDir"`))
			Expect(cue).To(ContainSubstring("emptyDir.medium: v.medium"))
		})

		It("should generate optional fields inside variant blocks", func() {
			volumes := defkit.List("volumes")
			comp := defkit.NewComponent("test").
				Workload("batch/v1", "Job").
				Params(volumes).
				Template(func(tpl *defkit.Template) {
					tpl.Output(
						defkit.NewResource("batch/v1", "Job").
							Set("spec.volumes",
								defkit.Each(volumes).
									Map(defkit.FieldMap{"name": defkit.FieldRef("name")}).
									MapVariant("type", "configMap", defkit.FieldMap{
										"configMap.name": defkit.FieldRef("cmName"),
										"configMap.items": defkit.OptionalFieldRef("items"),
									}),
							),
					)
				})

			cue := gen.GenerateFullDefinition(comp)

			Expect(cue).To(ContainSubstring(`if v.type == "configMap"`))
			Expect(cue).To(ContainSubstring("configMap.name: v.cmName"))
			Expect(cue).To(ContainSubstring("if v.items != _|_"))
			Expect(cue).To(ContainSubstring("configMap.items: v.items"))
		})
	})

	Describe("Multi-conditional leaf values (condValues)", func() {
		It("should render both conditional values when same path is set with different conditions", func() {
			gen := defkit.NewCUEGenerator()

			volMounts := defkit.String("volumeMounts")
			volumes := defkit.String("volumes")

			comp := defkit.NewComponent("test-multi-cond").
				Description("Test multi-conditional values").
				AutodetectWorkload().
				Params(volMounts, volumes).
				Template(func(tpl *defkit.Template) {
					res := defkit.NewResource("batch/v1", "Job")
					res.
						If(volMounts.IsSet()).
						Set("spec.containers[0].volumeMounts", defkit.Lit("mountsArray")).
						EndIf().
						If(volumes.IsSet()).
						Set("spec.containers[0].volumeMounts", defkit.Lit("volumesArray")).
						EndIf()
					tpl.Output(res)
				})

			cue := gen.GenerateFullDefinition(comp)
			// Both conditions should appear for the same field
			Expect(cue).To(ContainSubstring(`if parameter["volumeMounts"] != _|_`))
			Expect(cue).To(ContainSubstring(`if parameter["volumes"] != _|_`))
			// The field should appear twice, each in its own if block
			Expect(strings.Count(cue, "volumeMounts:")).To(BeNumerically(">=", 2))
		})

		It("should not activate condValues when path is set unconditionally then conditionally", func() {
			gen := defkit.NewCUEGenerator()

			optional := defkit.String("opt")

			comp := defkit.NewComponent("test-uncond-then-cond").
				Description("Test unconditional then conditional").
				AutodetectWorkload().
				Params(optional).
				Template(func(tpl *defkit.Template) {
					res := defkit.NewResource("v1", "Pod")
					res.
						Set("spec.field", defkit.Lit("default")).
						SetIf(optional.IsSet(), "spec.field", optional)
					tpl.Output(res)
				})

			cue := gen.GenerateFullDefinition(comp)
			// Conditional should win (overwrites unconditional)
			Expect(cue).To(ContainSubstring("field: parameter.opt"))
		})
	})

	Describe("Intermediate node decomposition (canDecomposeByCondition)", func() {
		It("should decompose struct into per-condition blocks when all leaves share the same condition set", func() {
			gen := defkit.NewCUEGenerator()

			cpu := defkit.String("cpu")
			memory := defkit.String("memory")

			comp := defkit.NewComponent("test-decompose").
				Description("Test condition decomposition").
				AutodetectWorkload().
				Params(cpu, memory).
				Template(func(tpl *defkit.Template) {
					res := defkit.NewResource("v1", "Pod")
					res.
						If(cpu.IsSet()).
						Set("spec.resources.limits.cpu", cpu).
						Set("spec.resources.requests.cpu", cpu).
						EndIf().
						If(memory.IsSet()).
						Set("spec.resources.limits.memory", memory).
						Set("spec.resources.requests.memory", memory).
						EndIf()
					tpl.Output(res)
				})

			cue := gen.GenerateFullDefinition(comp)
			// resources should NOT appear unconditionally
			// It should appear inside if blocks
			Expect(cue).To(ContainSubstring(`if parameter["cpu"] != _|_ {`))
			Expect(cue).To(ContainSubstring(`if parameter["memory"] != _|_ {`))

			// Each condition block should contain resources with limits and requests
			// Find the cpu block
			cpuIdx := strings.Index(cue, `if parameter["cpu"] != _|_ {`)
			Expect(cpuIdx).To(BeNumerically(">", 0))
			// After the cpu condition, resources should appear
			afterCpu := cue[cpuIdx:]
			Expect(afterCpu).To(ContainSubstring("resources:"))
			Expect(afterCpu).To(ContainSubstring("limits:"))
			Expect(afterCpu).To(ContainSubstring("requests:"))
		})

		It("should not decompose when children have unconditional values", func() {
			gen := defkit.NewCUEGenerator()

			cpu := defkit.String("cpu")

			comp := defkit.NewComponent("test-no-decompose").
				Description("Test no decomposition").
				AutodetectWorkload().
				Params(cpu).
				Template(func(tpl *defkit.Template) {
					res := defkit.NewResource("v1", "Pod")
					res.
						If(cpu.IsSet()).
						Set("spec.resources.limits.cpu", cpu).
						EndIf().
						Set("spec.resources.limits.memory", defkit.Lit("128Mi"))
					tpl.Output(res)
				})

			cue := gen.GenerateFullDefinition(comp)
			// resources should appear as a regular struct (not decomposed)
			// because it has a mix of conditional and unconditional children
			Expect(cue).To(ContainSubstring("resources:"))
			Expect(cue).To(ContainSubstring("limits:"))
			Expect(cue).To(ContainSubstring(`memory: "128Mi"`))
		})
	})

	Describe("GenerateFullDefinition", func() {
		It("should generate complete CUE definition", func() {
			comp := defkit.NewComponent("webservice").
				Description("Web service component").
				Workload("apps/v1", "Deployment").
				Params(
					defkit.String("image").Required(),
				)

			cue := gen.GenerateFullDefinition(comp)

			Expect(cue).To(ContainSubstring(`webservice: {`))
			Expect(cue).To(ContainSubstring(`type: "component"`))
			Expect(cue).To(ContainSubstring(`description: "Web service component"`))
			Expect(cue).To(ContainSubstring(`apiVersion: "apps/v1"`))
			Expect(cue).To(ContainSubstring(`kind:       "Deployment"`))
			Expect(cue).To(ContainSubstring(`type: "deployments.apps"`))
			Expect(cue).To(ContainSubstring(`parameter: {`))
		})

		It("should quote component names with special characters", func() {
			comp := defkit.NewComponent("my-service").
				Description("Service with dash").
				Workload("apps/v1", "Deployment")

			cue := gen.GenerateFullDefinition(comp)

			Expect(cue).To(ContainSubstring(`"my-service": {`))
		})

		It("should include status when defined", func() {
			comp := defkit.NewComponent("test").
				Workload("apps/v1", "Deployment").
				CustomStatus("message: \"Ready\"").
				HealthPolicy("isHealth: true")

			cue := gen.GenerateFullDefinition(comp)

			Expect(cue).To(ContainSubstring("status: {"))
			Expect(cue).To(ContainSubstring("customStatus:"))
			Expect(cue).To(ContainSubstring("healthPolicy:"))
		})

		It("should infer correct workload types", func() {
			testCases := []struct {
				apiVersion   string
				kind         string
				expectedType string
			}{
				{"apps/v1", "Deployment", "deployments.apps"},
				{"apps/v1", "StatefulSet", "statefulsets.apps"},
				{"apps/v1", "DaemonSet", "daemonsets.apps"},
				{"batch/v1", "Job", "jobs.batch"},
				{"batch/v1", "CronJob", "cronjobs.batch"},
			}

			for _, tc := range testCases {
				comp := defkit.NewComponent("test").
					Workload(tc.apiVersion, tc.kind)

				cue := gen.GenerateFullDefinition(comp)
				Expect(cue).To(ContainSubstring(tc.expectedType),
					"Expected workload type %s for %s/%s", tc.expectedType, tc.apiVersion, tc.kind)
			}
		})
	})

	Describe("GenerateParameterSchema with Map parameters", func() {
		It("should generate CUE for map parameters", func() {
			comp := defkit.NewComponent("test").
				Params(
					defkit.Map("labels").Description("Labels to apply"),
					defkit.Map("annotations").Optional(),
				)

			cue := gen.GenerateParameterSchema(comp)

			Expect(cue).To(ContainSubstring("labels?:"))
			Expect(cue).To(ContainSubstring("annotations?:"))
			Expect(cue).To(ContainSubstring("Labels to apply"))
		})
	})

	Describe("GenerateFullDefinition with template", func() {
		It("should generate output block with resource", func() {
			comp := defkit.NewComponent("test").
				Workload("apps/v1", "Deployment").
				Params(defkit.String("image")).
				Template(func(tpl *defkit.Template) {
					image := defkit.String("image")
					tpl.Output(
						defkit.NewResource("apps/v1", "Deployment").
							Set("metadata.name", defkit.VelaCtx().Name()).
							Set("spec.template.spec.containers[0].image", image),
					)
				})

			cue := gen.GenerateFullDefinition(comp)

			Expect(cue).To(ContainSubstring("output:"))
			Expect(cue).To(ContainSubstring("apiVersion:"))
			Expect(cue).To(ContainSubstring("kind:"))
		})

		It("should generate outputs block with auxiliary resources", func() {
			comp := defkit.NewComponent("test").
				Workload("apps/v1", "Deployment").
				Template(func(tpl *defkit.Template) {
					tpl.Output(
						defkit.NewResource("apps/v1", "Deployment").
							Set("metadata.name", defkit.VelaCtx().Name()),
					)
					tpl.Outputs("service",
						defkit.NewResource("v1", "Service").
							Set("metadata.name", defkit.VelaCtx().Name()),
					)
				})

			cue := gen.GenerateFullDefinition(comp)

			Expect(cue).To(ContainSubstring("output:"))
			Expect(cue).To(ContainSubstring("outputs:"))
			Expect(cue).To(ContainSubstring("service:"))
		})

		It("should generate conditional fields with SetIf", func() {
			cpu := defkit.String("cpu").Optional()
			comp := defkit.NewComponent("test").
				Workload("apps/v1", "Deployment").
				Params(cpu).
				Template(func(tpl *defkit.Template) {
					tpl.Output(
						defkit.NewResource("apps/v1", "Deployment").
							SetIf(cpu.IsSet(), "spec.resources.limits.cpu", cpu),
					)
				})

			cue := gen.GenerateFullDefinition(comp)

			Expect(cue).To(ContainSubstring("output:"))
			Expect(cue).To(ContainSubstring("if"))
		})
	})

	Describe("Import detection", func() {
		It("should detect strconv import from FormatInt", func() {
			port := defkit.Int("port")
			comp := defkit.NewComponent("test").
				Workload("apps/v1", "Deployment").
				Params(port).
				Template(func(tpl *defkit.Template) {
					tpl.Output(
						defkit.NewResource("apps/v1", "Deployment").
							Set("metadata.annotations.port", defkit.StrconvFormatInt(port, 10)),
					)
				})

			cue := gen.GenerateFullDefinition(comp)

			Expect(cue).To(ContainSubstring("strconv"))
		})

		It("should detect strings import from ToLower", func() {
			name := defkit.String("name")
			comp := defkit.NewComponent("test").
				Workload("apps/v1", "Deployment").
				Params(name).
				Template(func(tpl *defkit.Template) {
					tpl.Output(
						defkit.NewResource("apps/v1", "Deployment").
							Set("metadata.name", defkit.StringsToLower(name)),
					)
				})

			cue := gen.GenerateFullDefinition(comp)

			Expect(cue).To(ContainSubstring("strings"))
		})
	})
})
