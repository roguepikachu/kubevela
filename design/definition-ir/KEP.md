# KEP: Defkit Schematic Runtime (`ToDefkit` / `schematic.defkit`)

**Status:** Proven PoC / design proposal for upstreaming  
**Authors:** Ayush Kumar ([@roguepikachu](https://github.com/roguepikachu))  
**Created:** 2026-08-12  
**Last updated:** 2026-08-13  
**Base:** `17f55a794` (`origin/master`)  
**Branch / worktree:** `poc/definition-ir`  

**Related:**
- [kep-defkit.md](../vela-cli/kep-defkit.md) (Go authoring SDK; historically emits CUE)
- [sdk_generating.md](../vela-cli/sdk_generating.md) (GEN_SDK: typed Application consumers)
- [KEP-2.16 SourceDefinition](../vela-core/keps/2.16-source-definition/) (CEL for Application source expressions)
- Companion notes in this directory: [RESEARCH.md](./RESEARCH.md), [DECISION.md](./DECISION.md), [COMPATIBILITY.md](./COMPATIBILITY.md), [VALIDATION.md](./VALIDATION.md)

---

## Summary

KubeVela today stores and evaluates X-Definitions primarily as CUE templates (`spec.schematic.cue.template`). That design made Definitions powerful, but it also made CUE a hard dependency for authoring, schema export, upgrades, and controller execution at once.

**defkit** already lets platform engineers author Definitions in Go with IDE support. Its original design still compiled that Go into CUE strings for the controller. This KEP proposes the missing half of that story:

1. Keep the **existing defkit fluent API** as the only authoring surface.
2. Add **`ToDefkit()` / `ToDefkitYAML()`**, which lower builders into a declarative IR document.
3. Store that document as **`spec.schematic.defkit`** (`apiVersion: defkit.oam.dev/v1alpha1`).
4. Evaluate it with a **native Go declarative engine** (`pkg/defschematic/eval`) under capability category `defkit`.
5. Keep **`ToCue()`** as a first-class alternate emit so mixed-mode clusters remain safe.

In short: authoring stays Go, runtime becomes data plus an allowlisted interpreter, and CUE remains available for compatibility rather than as the only path.

A PoC on this branch has proven the path for Component, Trait, and Policy definitions; for richer template ops (conditionals, spreads, helpers, validators, native health/status); and for live clusters including a real cloud object-store claim reconciled by Crossplane.

---

## Motivation

### What hurts today

Platform teams that already live in Go and Kubernetes still have to become CUE experts to ship X-Definitions. That cost shows up as:

1. **Learning and review burden.** Reviews happen on generated or hand-written CUE, not on the Go that authors actually understand.
2. **Upgrade coupling.** Bumping `cuelang.org/go` or CueX behavior forces Definition churn even when Application APIs did not change (for example the CUE v0.14 line of work).
3. **Debuggability.** Failures surface as CUE unification or CueX provider errors far from the authoring intent.
4. **Incomplete earlier solutions.**
   - **GEN_SDK** improves Application *consumption* (typed clients). It does not remove CUE from Definition runtime.
   - **defkit (1.x intent)** improves Definition *authoring*, but kep-defkit explicitly treated controller CUE as out of scope. Authors still ship CUE into the cluster.

### Why this is the right seam

KubeVela already separates “what the user wrote” from “how the controller evaluates it” in other places. KEP-2.16 moved Application source expressions toward CEL. This KEP applies the same idea to X-Definitions: **compile-time authoring** versus **declarative runtime data**.

Cross-project patterns point the same way: CDK and Crossplane compositions compile or lower intent before the control plane runs; CEL is preferred over embedding a full language for policy-ish evaluation. None of that requires deleting CUE tomorrow. It does require a first-class non-CUE schematic branch.

---

## Goals

1. Provide a versioned **defkit schematic IR** covering Component, Trait, Policy, and WorkflowStep shapes needed for representative Definitions.
2. Evaluate those Definitions **without executing CUE templates** for the definition body.
3. Keep Go execution **compile-time only** (CLI / module build / `goloader`). No user Go in the controller.
4. Support **mixed-mode** clusters: legacy `schematic.cue` and new `schematic.defkit` side by side.
5. Preserve the **single fluent API** (`defkit.NewComponent` / `NewTrait` / …). Do not invent a parallel authoring kit.
6. Document migration, CRD requirements, security, and honesty about gaps (foreach, CueX providers, full workflow TaskRunner).

## Non-Goals

1. Flag-day removal of CUE from KubeVela.
2. Full CueX provider parity (`#do` HTTP, cloud provider helpers, and friends) in the first milestone.
3. Replacing VelaQL, Terraform schematic, or every health policy in existence on day one.
4. Multi-language authoring beyond Go.
5. Imperative in-cluster plugins or arbitrary script execution as the Definition runtime.
6. Rewriting every stock Definition in the ecosystem before the engine is production-ready.

---

## Background

See [RESEARCH.md](./RESEARCH.md) for the longer inventory. The short version:

| Layer | Role of CUE today |
|-------|-------------------|
| Storage | `schematic.cue.template` on X-Definitions |
| Schema | Parameter constraints + OpenAPI export |
| Templating | `output` / `outputs` / patches |
| Runtime | CueX compile + providers during reconcile |
| Workflow | Step templates and data passing |

defkit today is a **Go → CUE** compiler. GEN_SDK is a **CUE/Application → Go SDK** generator. Neither separates Definition authoring from Definition execution by itself.

This proposal names the missing artifact **defkit schematic**: a JSON document the controller can interpret without CueX for the definition body.

---

## Problem statement

| Area | Failure mode |
|------|----------------|
| UX | Authors think in Go; the cluster stores and fails in CUE |
| Runtime | Value passing and merges are hard to explain without CUE mental models |
| Maintenance | Definition semantics drift when the CUE toolchain drifts |
| Versioning | “Definition version” and “CUE language version” are entangled |
| Ecosystem | Almost every extension assumes `schematic.cue` is the only schematic |

---

## Design alternatives considered

Decision matrix detail lives in [RESEARCH.md](./RESEARCH.md) and [DECISION.md](./DECISION.md).

| Option | Outcome |
|--------|---------|
| Stay on CUE only; improve docs and tooling | Rejected as the sole long-term path |
| Improve defkit CUE emit only | Necessary for compatibility; insufficient for runtime independence |
| Parallel fluent API (“dirkit”) next to defkit | Rejected; dual APIs split the ecosystem. Spike code was deleted |
| Replace CUE with Jsonnet / KCL / Dhall wholesale | Rejected; new language tax without solving authoring/runtime split |
| Imperative Go plugins loaded by the controller | Rejected on security and operability grounds |
| **defkit fluent → `ToDefkit` → `schematic.defkit` → native eval; `ToCue` alternate** | **Accepted** |

---

## Proposed architecture

```text
Developer (compile time)
        |
        v
defkit fluent API
  NewComponent / NewTrait / NewPolicy / NewWorkflowStep
        |
        +-- ToCue() / ToYAML()           --> schematic.cue        (compatibility)
        |
        +-- ToDefkit() / ToDefkitYAML()  --> schematic.defkit
        |                                      apiVersion: defkit.oam.dev/v1alpha1
        |                                      kind: Component | Trait | Policy | WorkflowStep
        |
        +-- Param decls --> OpenAPI ConfigMap (no CueX required)
        |
        v
Cluster X-Definition CR
        |
        v
appfile loadSchematicToTemplate
  if schematic.defkit != nil -> CapabilityCategory = defkit
        |
        v
pkg/defschematic/eval (AbstractEngine adapters)
        |
        v
Unstructured Kubernetes resources (+ traits/patches)
```

Optional: `pkg/defschematic/cuebridge` can best-effort render IR back toward CUE for migration tooling. It is not required on the hot path once the controller speaks `schematic.defkit`.

### Schematic API

```go
// apis/core.oam.dev/common
type Defkit struct {
    // Template holds a JSON-encoded ir.Definition document.
    Template string `json:"template"`
}

type Schematic struct {
    CUE       *CUE       `json:"cue,omitempty"`
    Terraform *Terraform `json:"terraform,omitempty"`
    Defkit    *Defkit    `json:"defkit,omitempty"`
}
```

CRD OpenAPI for ComponentDefinition, TraitDefinition, PolicyDefinition, WorkflowStepDefinition, DefinitionRevision, and ApplicationRevision **must** include nested `schematic.defkit`. If revision CRDs omit it, Kubernetes prunes the field on store, and trait/component evaluation falls back into broken CUE paths (`field not found: output` was the PoC failure mode).

### Runtime selection

1. Parser / template load sees `schematic.defkit` and sets `DefkitCategory`.
2. Workload and trait engines are constructed from `pkg/defschematic/eval` instead of CueX AbstractEngines.
3. Parameter validation uses IR param decls (`ir.ValidateParams` / `ValidateParamsFull`) including defaults, enums, nested objects, conditional param blocks, and named validators.
4. Health and custom status can be carried as native IR (`Health` / `Status` specs) and evaluated against `context.output` without a CUE health policy string.

### Declarative semantics

Builders **record** operations. They do not close over runtime cluster state. The evaluator interprets an allowlisted IR:

**Expressions:** literals, param paths (including dotted nested paths), context / input refs, concat / plus, object / list, helper refs, status field refs.

**Conditions:** isset, eq / ne, comparisons, and / or / not, path exists, matches, length checks.

**Field ops:** set, set-if, spread-if (merge map into path), conditional struct (nested field groups).

**Helpers:** named precomputations such as claim-style name construction with length limits (for example truncate-and-hash when exceeding DNS-1123 length budgets).

**No user Go** runs in-cluster. Expanding power means expanding the allowlist and tests, not shipping binaries as Definitions.

---

## Detailed design

### Authoring (unchanged fluency)

Authors continue to write:

```go
func DefkitWebservice() *defkit.ComponentDefinition {
    image := defkit.String("image").Required()
    replicas := defkit.Int("replicas").Default(1)

    return defkit.NewComponent("defkit-webservice").
        Description("PoC webservice via ToDefkit").
        Workload("apps/v1", "Deployment").
        Params(image, replicas).
        Template(func(tpl *defkit.Template) {
            vela := defkit.VelaCtx()
            deploy := defkit.NewResource("apps/v1", "Deployment").
                Set("metadata.name", vela.Name()).
                Set("spec.replicas", replicas).
                Set("spec.template.spec.containers[0].image", image)
            tpl.Output(deploy)
            // secondary resources via Outputs...
        })
}
```

Emit for cluster:

```go
yaml, err := DefkitWebservice().ToDefkitYAML()
```

Emit for CUE-only environments:

```go
cue := DefkitWebservice().ToCue()
```

Module registration can choose emit mode via `DEFKIT_EMIT=cue|defkit`. `goloader` applies `schematic.defkit` when the defkit payload is present (`FromDefkitTemplate`).

### IR document (conceptual)

```json
{
  "apiVersion": "defkit.oam.dev/v1alpha1",
  "kind": "Component",
  "name": "defkit-webservice",
  "params": [ { "name": "image", "type": "string", "required": true } ],
  "template": {
    "output": {
      "apiVersion": "apps/v1",
      "kind": "Deployment",
      "fields": [
        { "path": "spec.replicas", "value": { "param": "replicas" } }
      ]
    },
    "outputs": { }
  },
  "health": { "type": "crossplaneClaim" },
  "status": {
    "healthyMessage": { "plus": [ { "lit": "ready: " }, { "statusField": "metadata.name" } ] }
  }
}
```

Packages: `pkg/defschematic/ir` (types + validation), `pkg/defschematic/eval` (engine), `pkg/definition/defkit/defkitgen.go` (lowering).

### Evaluation pipeline

1. Parse JSON IR from `schematic.defkit.template`.
2. Validate and default parameters; apply active conditional param branches and validators.
3. Bind helpers into the evaluation environment.
4. Render `template.output` and `template.outputs` onto `unstructured.Unstructured`.
5. For traits, apply patch field sets onto the component result.
6. For status requests, evaluate native health/status against live `context.output` (for example Ready and Synced conditions on claim-shaped workloads, plus message templates).
7. Adapter note (PoC): rendered JSON may still be wrapped through existing `process.Context` helpers that historically spoke CUE `Instance`. That is glue for the Application controller, not “the definition ran as CUE.”

### Workflow steps

Native `EvalWorkflowStep` exists and is unit-tested for simple step I/O. Wiring a CueX-free TaskRunner on the live workflow engine is **future work**. Cluster proofs used stock `apply-component` steps successfully with defkit Components/Traits/Policies.

---

## Compatibility and migration

### Mixed mode

| Definition schematic | Controller behavior |
|----------------------|---------------------|
| `schematic.cue` only | Unchanged CueX path |
| `schematic.defkit` present | DefkitCategory + native eval |
| Both present | Prefer explicit product policy; PoC treats defkit as selected when set |

Existing Applications keep working. OpenAPI ConfigMaps for defkit params are generated without CueX (`openAPIFromDefkit` in the capability controller path).

### Migration steps for adopters

1. Upgrade CRDs so revision objects retain `schematic.defkit`.
2. Author or port Definitions with defkit; emit `ToDefkitYAML()`.
3. Roll a controller build that understands `DefkitCategory`.
4. Deploy Definitions and Applications in a canary namespace/cluster.
5. Keep `ToCue()` available for clusters not yet upgraded (at least two major releases recommended).
6. Grow IR ops against real Definition corpora; track coverage in the compatibility matrix.

### Rollout requirement (hard)

ApplicationRevision and DefinitionRevision schemas must nest `schematic.defkit`. This is not optional polish. PoC measured empty schematics after prune, followed by trait evaluation failures.

---

## Security considerations

| Threat | Mitigation |
|--------|------------|
| Arbitrary code in Definitions | IR is data; interpreter allowlist only |
| Unexpected network / cloud I/O from templates | No CueX-style provider ops in PoC Expr set |
| Malicious Definition publish | Same trust boundary as today: who can create ComponentDefinitions |
| Supply chain | Go modules resolve at **build** of the Definition package and of the controller, not by downloading code during reconcile |
| Resource abuse | Standard Kubernetes admission / quota; evaluator should keep bounded work (future: explicit op budgets) |

---

## Performance

PoC unit renders for sample Definitions complete in well under a second on developer hardware (`go test ./pkg/defschematic/eval`).  

Controller-level comparative benchmarks versus CueX (p50/p99 reconcile, binary size, memory) are **not yet validated** and remain a gate for production investment.

---

## Testing strategy

| Layer | What |
|-------|------|
| Unit IR / eval | Component, trait patch, policy, workflow step, param validation, golden outputs |
| Lowering | `ToDefkit` on representative fluent Definitions; `ToCue` still works as alternate |
| Manifest gen | `go run ./hack/defkit-poc/gen_manifests.go` |
| Local cluster | k3d with local image; webservice + scaler + policy Application to `running` |
| Richer Definitions | Conditional structs, map spreads, claim-name helpers, validators, native health messages |
| Live cloud (optional proof) | Crossplane-backed object-store claim Application; confirm Ready/Synced and that the cloud resource exists |

Evidence files: [VALIDATION.md](./VALIDATION.md), [COMPATIBILITY.md](./COMPATIBILITY.md).

---

## PoC results (evidence)

**Identity:** base `17f55a794`, branch `poc/definition-ir`, packages under `pkg/defschematic` and `pkg/definition/defkit`.

### What passed

1. **Authoring convergence.** One fluent API; `ToDefkitYAML()` produces cluster manifests; `ToCue()` remains viable.
2. **Core kinds on cluster (k3d).** Application with defkit Component + Trait + Policy reached `running` with workflow `succeeded`. Deployments, Services, and a policy ConfigMap matched intent (including trait replica patch).
3. **Revision fidelity.** After CRD patches, ApplicationRevision / DefinitionRevision retained `schematic.defkit`.
4. **Richer IR.** Plus / concat naming, spread-if into maps (including bracketed map keys), conditional nested structs, conditional params, validators, and claim-name helpers evaluate correctly in unit tests.
5. **Native health/status.** Crossplane-style Ready+Synced health and dynamic status messages work end-to-end (no permanent “status eval not implemented” stub for Definitions that declare IR health).
6. **Live cloud proof (2026-08-13).** Controller image built from this branch was rolled onto a shared EKS cluster (Flux HelmReleases for the operator suspended for the experiment). A ComponentDefinition emitted as `schematic.defkit` rendered an object-store claim. The claim became Synced/Ready, Application healthy, and the corresponding cloud bucket was observed via AWS APIs (region and tags matched the rendered claim).

### Known gaps (still honest)

1. Foreach / iteration ops are not in the IR.
2. CueX providers are intentionally absent.
3. Workflow steps are not registered as a fully CueX-free TaskRunner on the cluster workflow engine.
4. Some `process.Context` adapter code still touches CUE `Instance` types for glue.
5. Broad stock-Definition coverage and reconcile benchmarks are unfinished.

### Historical naming note

An early spike used names DIR / dirkit / `schematic.dir`. That parallel API was abandoned. The locked names are **defkit schematic**, **`ToDefkit`**, and **`schematic.defkit`**.

---

## Open questions

1. Should large IR documents live inline in the CR, or be referenced from OCI / ConfigMap for size limits?
2. Should a subset of Expr/Condition move to CEL for alignment with KEP-2.16, or stay a closed Go AST for predictability?
3. When both `schematic.cue` and `schematic.defkit` are set, is the precedence rule “defkit wins,” “cue wins,” or “reject as invalid”?
4. What is the compatibility window for `ToCue()` once defkit schematic is GA?

---

## Future work

1. CueX-free workflow TaskRunner integration.
2. Controlled provider op surface (explicit, allowlisted, auditable).
3. Foreach and richer patch strategies with differential tests against stock Definitions.
4. Coverage expansion across the public Definition corpus.
5. Reconcile latency and memory benchmarks versus CueX.
6. Upstream CRD / API review for `schematic.defkit` stabilization (`v1alpha1` → eventual GA).

---

## Recommendation

**Accept Defkit 2.0 as the target architecture for CUE-independent Definition runtime**, implemented as:

`defkit fluent API → ToDefkit / schematic.defkit → pkg/defschematic/eval`, with `ToCue` retained for compatibility.

Do **not** pursue flag-day CUE deletion, a second authoring SDK, or in-cluster Go plugins.

Treat nested `schematic.defkit` on revision CRDs as a **release blocker** for any cluster rollout.

Invest next in workflow TaskRunner wiring, IR coverage against real Definitions, and controller benchmarks before declaring GA.

See [DECISION.md](./DECISION.md) for the short decision record that matches this recommendation.

---

## Appendix A: Package map (PoC)

| Path | Role |
|------|------|
| `apis/core.oam.dev/common` | `Schematic.Defkit` API types + generated deepcopy |
| `charts/vela-core/crds/*` | OpenAPI including nested `schematic.defkit` |
| `pkg/definition/defkit` | Fluent API + `ToDefkit` lowering + `ClaimName` helper |
| `pkg/defschematic/ir` | IR types and param validation |
| `pkg/defschematic/eval` | Native engines (component/trait/policy/workflow/status) |
| `pkg/defschematic/cuebridge` | Optional IR→CUE aid |
| `pkg/defschematic/pocdefs` | Example Definitions used in tests and manifest gen |
| `pkg/appfile`, `pkg/controller/utils` | Category selection, OpenAPI, status wiring |
| `hack/defkit-poc/` | Manifest generator and cluster examples |

## Appendix B: Example cluster apply (local)

```bash
go test ./pkg/defschematic/... ./pkg/definition/defkit/... -count=1
go run ./hack/defkit-poc/gen_manifests.go ./hack/defkit-poc/examples
# build controller image; apply CRDs; apply examples; watch Application status
```

Reproduce details and measured outputs: [README.md](./README.md), [VALIDATION.md](./VALIDATION.md).
