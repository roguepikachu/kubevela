# KEP: Defkit Schematic (`ToDefkit` / `schematic.defkit`) for CUE-Independent Runtime

**Status:** Experimental / PoC  
**Authors:** KubeVela platform engineering (Defkit 2.0 PoC)  
**Base:** `17f55a794` (`origin/master`)  
**Related:** [kep-defkit.md](../vela-cli/kep-defkit.md), [sdk_generating.md](../vela-cli/sdk_generating.md), [KEP-2.16 SourceDefinition](../vela-core/keps/2.16-source-definition/), [RESEARCH.md](./RESEARCH.md)

## Summary

KubeVela today stores and evaluates X-Definitions as CUE (`spec.schematic.cue.template`), which couples user authoring, schema export, and controller runtime to the CUE/CueX dependency surface. **defkit** improved Go authoring but still emits CUE for runtime. This KEP proposes separating **authoring** from **execution** via **`schematic.defkit`** (defkit schematic IR) produced by **`ToDefkit()`**, evaluated by a native Go declarative engine, with CUE retained as a legacy/compatibility backend.

## Motivation

1. **User DX:** Platform engineers who know Go/Kubernetes must learn CUE before writing Definitions ([Observed] defkit KEP motivation).
2. **Maintenance:** CUE upgrades (e.g. `#6877` → v0.14.1) force broad template and CueX churn ([Observed]).
3. **Runtime coupling:** CueX is schema + template + providers + constraints in one language ([Observed] `pkg/cue/definition/template.go`).
4. **Prior attempts:** GEN_SDK solves Application *consumption*; defkit solves authoring but not runtime independence ([Observed]).

## Goals

- Provide a declarative IR for Component/Trait/Policy/WorkflowStep definitions.
- Evaluate representative definitions **without executing CUE templates**.
- Keep Go authoring compile-time only (no arbitrary Go in the control plane).
- Support mixed-mode clusters (legacy CUE + schematic.defkit).
- Document migration, security, and compatibility honestly.

## Non-Goals

- Full CueX provider parity in the PoC.
- Replacing VelaQL / Terraform schematic / health CUE in the first milestone.
- Multi-language authoring beyond Go.
- Flag-day removal of CUE.

## Background

See [RESEARCH.md](./RESEARCH.md). Short version:

- CUE is multi-role in KubeVela.
- defkit = Go → CUE generation layer.
- GEN_SDK = CUE → typed Application SDK.
- KEP-2.16 already moved *source expressions* toward CEL, showing per-layer CUE reduction.

## Problem Statement

| Area | Problem |
|------|---------|
| UX | CUE learning curve; weak IDE vs Go |
| Runtime | Awkward value passing; hard-to-debug merges |
| Maintenance | Upgrade coupling to `cuelang.org/go` + CueX |
| Versioning | Definition semantics tied to CUE language changes |
| Ecosystem | Extensions assume CUE schematic only |

## Design Alternatives Considered

Decision matrix in RESEARCH.md. Selected: **defkit fluent API → ToDefkit/schematic.defkit → native evaluator; ToCue alternate**. Rejected as sole strategies: stay-on-CUE, defkit-only, GEN_SDK, imperative Go plugins, wholesale Jsonnet/KCL swap.

## Proposed Architecture

```text
Developer
   |
   v
defkit fluent API (compile-time)
   |
   +-- ToCue() / ToYAML()      → schematic.cue
   +-- ToDefkit() / ToDefkitYAML() → schematic.defkit
                                      (apiVersion defkit.oam.dev/v1alpha1)
   |
   +--> OpenAPI (from Param decls)
   +--> pkg/defschematic/eval (native declarative evaluator)
   +--> pkg/defschematic/cuebridge (optional migration aid)
   |
   v
KubeVela appfile / AbstractEngine (DefkitCategory)
```

### Schematic API

```go
type Defkit struct {
  Template string `json:"template"` // JSON ir.Definition
}
type Schematic struct {
  CUE *CUE `json:"cue,omitempty"`
  Terraform *Terraform `json:"terraform,omitempty"`
  Defkit *Defkit `json:"defkit,omitempty"`
}
```

### Runtime selection

`loadSchematicToTemplate` sets `CapabilityCategory=dir` when `schematic.defkit` is present. Parser selects `defschematic/eval.NewWorkloadEngine` / `NewTraitEngine` instead of CUE AbstractEngine.

### Declarative semantics

Builders **record** ops (field sets, patches, expressions). The evaluator interprets an allowlisted Expr set (`lit`, `param`, `context`, `input`, `template`, `concat`, `object`, `list`). No user Go executes in-cluster.

## Detailed API Design

### Current CUE (sketch)

```cue
parameter: {
  image: string
  replicas: *1 | int
}
output: {
  apiVersion: "apps/v1"
  kind: "Deployment"
  spec: replicas: parameter.replicas
  // ...
}
```

### Existing defkit

```go
defkit.NewComponent("webservice").
  Params(image, replicas).
  Template(func(tpl *defkit.Template) { tpl.Output(...) })
// → ToCue() → CUE string → controller CueX
```

### Proposed defkit ToDefkit path

```go
defkit.NewComponent("defkit-webservice").
  Params(defkit.Required(defkit.StringParam("image")), ...).
  Output(defkit.Resource("apps/v1", "Deployment",
    defkit.Set("spec.replicas", defkit.ParamRef("replicas")),
  ))
// → JSON defkit schematic → native eval
```

Concrete examples: `pkg/dir/pocdefs`, manifests under `hack/dir-poc/examples/`.

## Runtime Model

1. Validate/default params (`ir.ValidateParams`).
2. Build env `{Params, Context, Inputs}`.
3. Render `output` / `outputs` via field setters.
4. Traits apply `patches` onto existing unstructured objects.
5. Wrap results as `model.Instance` via JSON→cue compile **only as process.Context adapter** (documented glue; definition body is not CUE).

Workflow steps: native `EvalWorkflowStep` for PoC logic; cluster TaskRunner wiring remains future work (Partial).

## Compatibility

Mixed mode: CUE definitions unchanged. defkit schematic definitions use new schematic field and CRD schema additions.

## Migration Strategy

1. Author new defs with defkit `ToDefkitYAML()`; ship `schematic.defkit`.
2. Optionally emit CUE via cuebridge for environments not yet upgraded.
3. Evolve defkit `ToDefkit()` as convergence path.
4. Grow evaluator ops (foreach, providers) capability-by-capability.
5. Keep CUE backend for years; deprecate only after coverage metrics warrant.

## Backward Compatibility

Existing Applications and CUE Definitions continue to work. OpenAPI ConfigMaps for defkit schematic params generated without CueX (`openAPIFromDefkit`).

## Performance

PoC smoke: defkit schematic unit render is sub-second for sample defs ([Measured] `go test ./pkg/defschematic/eval`). Full comparative controller benchmarks: Not yet validated.

## Security

| Threat | Mitigation |
|--------|------------|
| Arbitrary Go in controller | Forbidden; defkit schematic is data |
| Unbounded I/O | No provider ops in PoC Expr allowlist |
| Malicious defkit schematic | Same trust as publishing ComponentDefinitions today |
| Supply chain | Go modules at build time only |

## Testing

- Unit: `pkg/dir/eval` (component, trait, policy, workflow, validation, golden)
- Manifest gen: `go run ./hack/dir-poc/gen_manifests.go`
- Live: see VALIDATION.md

## PoC Results

See [COMPATIBILITY.md](./COMPATIBILITY.md) and [VALIDATION.md](./VALIDATION.md).

Live k3d (`dir-poc`, 2026-08-12) with image `vela-core:dir-poc`:

- Application `dir-poc-app` → **`running`**, workflow **`succeeded`**, Ready `True`.
- defkit schematic component `defkit-webservice` created Deployments + Services for `frontend` and `backend`.
- defkit schematic trait `defkit-scaler` patched frontend `spec.replicas` to **2** (component default was 1).
- defkit schematic policy `defkit-override` created ConfigMap `show-override-defkit-policy` with `data.engine=defkit`.
- ApplicationRevision and DefinitionRevision retained `schematic.defkit` after CRD patches ([Measured]).
- Unit: `go test ./pkg/dir/...` Pass, including engine component→trait path.

Earlier failure mode ([Observed]): missing `schematic.defkit` on ApplicationRevision OpenAPI caused empty component schematic in the compressed revision; workflow then failed with `field not found: output` when evaluating `dir-scaler`. Re-applying CRDs and recreating the Application fixed it.

## Limitations

- No foreach / CueX / health parity (status stub only).
- Trait/component Instance glue still uses CUE `Encode` of rendered JSON for `process.Context`.
- Workflow step not registered as CueX-free TaskRunner (cluster used `apply-component`).
- `.vscode/scripts/k3d-*.sh` absent on `origin/master` (use Dockerfile + Obsidian runbook).
- CRDs for ApplicationRevision/DefinitionRevision **must** include nested `schematic.defkit` or revisions prune defkit schematic templates.

## Open Questions

1. Done in PoC: single defkit API; ToDefkit emit; dirkit deleted
2. Persist large IR in OCI vs inline CR JSON?
3. Adopt CEL for Expr subset (align with KEP-2.16)?

## Future Work

- Native workflow TaskRunner
- Provider op surface
- DynamoDB / Postgres / OpenSearch ports (same IR patterns as S3/EFS)
- defkit convergence
- Benchmarks vs CueX
- Live AWS Crossplane compositions (out of band for PoC)

## Recommendation

See [DECISION.md](./DECISION.md).
