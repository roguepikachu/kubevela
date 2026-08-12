> **Naming update:** Early drafts called this DIR/dirkit. Locked name is **defkit schematic** (`ToDefkit` / `schematic.defkit`).

# Research Report: Reducing CUE Dependency for KubeVela Definition Authoring

**Base commit:** `17f55a794` (`origin/master`)  
**Worktree:** `/Users/aykumar/oam/opensource/kubevela-dir-poc`  
**Branch:** `poc/definition-ir`  
**Evidence tags:** Observed | Measured | Inferred | Hypothesized | Not yet validated

---

## 1. Executive finding

KubeVela uses CUE as **schema language, templating language, constraint system, composition engine, and runtime evaluator** simultaneously ([Observed] `apis/core.oam.dev/common/types.go`, `pkg/cue/definition/template.go`, CueX in `github.com/kubevela/pkg`).

**defkit** improves Go authoring DX but still **terminates in CUE strings** evaluated by the controller ([Observed] `pkg/definition/defkit/cuegen.go`, kep-defkit Non-Goal #2).

**GEN_SDK** solves the inverse problem (typed Application *consumers*), not definition authoring or runtime CUE load ([Observed] `design/vela-cli/sdk_generating.md`).

A credible next step is to **separate authoring from execution** via a **Definition Intermediate Representation (DIR)** with a native declarative evaluator and a CUE compatibility bridge. This is the architecture under PoC validation in this branch.

Adjacent work already moved **Application source expressions** from a hand-rolled / CUE path toward **CEL** (KEP-2.16 commits on this history: `feat(sources)!: replace the CUE expression engine with CEL`). That is further evidence that CUE-independence is being explored **per layer**, not as a flag-day rewrite ([Observed] git log on `origin/master`).

---

## 2. KubeVela CUE internals

### 2.1 Storage and load path [Observed]

| Kind | Schematic field | Load | Eval |
|------|-----------------|------|------|
| ComponentDefinition | `spec.schematic.cue.template` | `appfile.LoadTemplate` → `loadSchematicToTemplate` | `Component.EvalContext` → `WorkloadAbstractEngine.Complete` |
| TraitDefinition | same | same | `Trait.EvalContext` → `TraitAbstractEngine.Complete` |
| PolicyDefinition | same | same | Parsed as Component-like; resource policies use workload engine |
| WorkflowStepDefinition | same | `pkg/workflow/template/load.go` | kubevela/workflow runners + CueX providers |

`Schematic` today:

```go
type Schematic struct {
  CUE *CUE `json:"cue,omitempty"`
  Terraform *Terraform `json:"terraform,omitempty"`
}
```

### 2.2 Runtime injection [Observed]

Every reconcile concatenates template + `parameter: {...}` + `context: {...}` and compiles with CueX (`velacuex.WorkloadCompiler`). Context keys live in `pkg/cue/process/keyword.go` (`name`, `appName`, `namespace`, `cluster`, `artifacts`, …).

Traits additionally support `processing` (HTTP), `patch` / `patchOutputs` via CUE Unify, and `errs`.

### 2.3 Responsibility decomposition [Inferred from Observed]

| Responsibility | CUE used? | Requires declarative eval engine? | Notes |
|----------------|-----------|-----------------------------------|-------|
| Parameter schema + defaults | Yes | Yes (or OpenAPI/JSON Schema) | Also exported to OpenAPI ConfigMaps |
| Resource templating | Yes | Yes | `output` / `outputs` |
| Trait patch / unify | Yes | Yes (merge/patch semantics) | Today CUE Unify |
| Constraints / validation | Yes | Yes | Incomplete kinds + `errs` |
| Side-effect providers | CueX `#do` | Yes, but **not** CUE-specific | Could be Go providers behind IR ops |
| Health / customStatus | Yes | Yes (expressions over live objects) | |
| VelaQL / config templates | Yes | Separate product surfaces | Out of PoC |
| OpenAPI for UX | CUE→OpenAPI | No (can generate from IR schema) | |

**Conclusion:** CUE is convenient glue for many roles; only some roles *require* a general constraint language. Templating + schema + patch can be an IR + evaluator. CueX-style I/O is a **provider** problem, not a CUE-syntax problem.

### 2.4 Dependency surface [Measured]

From `go.mod` at base commit:

| Module | Version |
|--------|---------|
| `cuelang.org/go` | v0.14.1 |
| `github.com/kubevela/pkg` (CueX) | v1.11.1-0.20260722232534-455842713731 |
| `github.com/kubevela/workflow` | v0.7.3-0.20260724135823-5e2a5c1ecb21 |

Upgrade evidence: PR `#6877` (CUE → v0.14.1) touched definition templates, providers, deepcopy ordering, and parameter parsing safety ([Observed] `git show d627ecea2`). CueX adoption spanned multiple PRs (`#6575`, `#6720`, `#6799`, …).

---

## 3. CUE technical assessment (KubeVela-shaped)

| Capability | KubeVela needs it? | CUE required? | Alternative possible? |
|------------|--------------------|---------------|------------------------|
| Defaults (`*value \| type`) | Yes | No | IR default fields |
| Required / optional | Yes | No | Schema flags |
| Unification / open structs | Yes (patch) | No | Strategic merge / JSON merge / field ops |
| Comprehensions | Yes (lists) | No | `foreach` IR ops |
| Conditionals | Yes | No | `if` IR ops |
| References (`parameter.x`, `context.y`) | Yes | No | Path expressions in IR |
| CueX providers | Yes (workflow heavy) | No (dispatch shell today) | Native providers / IR ops |
| Closed schemas / enums | Yes | No | JSON Schema / OpenAPI |
| Error quality | Painful for users | N/A | Structured IR errors |
| Go embed API stability | Maintenance burden | Coupled | Smaller IR surface |
| IDE for CUE | Weak vs Go | — | Go authoring or schema editors |

Ergonomics for K8s/Go developers: steep ([Inferred] from defkit KEP motivation and community docs). Debugging compiled CUE merges is hard ([Observed] error formatting investment in `FormatCUEError`).

---

## 4. GEN_SDK autopsy [Observed]

**Location:** `pkg/definition/gen_sdk`, CLI `vela def gen-api`, design `design/vela-cli/sdk_generating.md`.

**Intent:** Generate typed Go SDKs for *assembling Applications* from existing X-Definition `parameter` schemas (CUE → OpenAPI → openapi-generator Docker → jennifer modifiers).

**Why it does not solve this KEP:**

- Source of truth remains CUE definitions.
- Runtime validation/render remains CUE.
- Depends on Dockerized openapi-generator (fragile in some environments; known local test skips).
- Audience is Application authors, not Definition authors.

**Lessons to keep:** typed Application assembly; OpenAPI as interchange; incremental generation.

**Do not repeat:** coupling definition evolution to OpenAPI-generator templates; treating consumer SDKs as an authoring strategy.

---

## 5. defkit (often misnamed “Defit”) autopsy [Observed]

**Location:** `pkg/definition/defkit` (~35 non-test Go files; `cuegen.go` ~4020 lines).  
**KEP:** `design/vela-cli/kep-defkit.md` (#7009), implementation (#7037+).  
**Consumer:** `vela-go-definitions` (RawCUE residual ~5 files measured via ripgrep).

**Architecture:**

```text
Fluent Go builders → recorded ops / expression builders → CUEGenerator → CUE string
→ Definition CR → controller CueX eval (unchanged)
```

**Solves:** IDE, types, unit tests, Go-module distribution, compile-time authoring (no Go in control plane).

**Does not solve:** runtime CUE dependency, CUE upgrade coupling, CueX complexity, RawCUE escape hatches for hard patterns.

**Can ideas extend to CUE-independent architecture?** Yes. defkit already builds an **implicit IR of operations**. The missing step is to **serialize that IR** and evaluate it natively instead of string-emitting CUE. That is exactly DIR.

**Verdict:** defkit is a better **authoring API**, currently implemented as a **CUE generation layer**. It is the right DX precursor, not the end state for CUE independence.

---

## 6. Alternatives matrix

Scores: 1 (poor) – 5 (excellent) for KubeVela definition authoring + runtime needs. [Inferred] from primary docs + fit to Observed KubeVela semantics.

| Candidate | Expressiveness | Declarative | Go/K8s fit | Maint. | Migration | Verdict |
|-----------|----------------|-------------|------------|--------|-----------|---------|
| Stay on CUE | 5 | 5 | 2 | 2 | 5 | Status quo pain |
| Improve defkit only | 4 | 5* | 5 | 3 | 4 | *runtime still CUE |
| GEN_SDK | 2 | 3 | 4 | 2 | 3 | Wrong problem |
| Pure imperative Go plugins | 5 | 1 | 5 | 2 | 1 | Reject (security/determinism) |
| Go fluent → DIR → native eval | 4 | 5 | 5 | 4 | 4 | **PoC target** |
| Go fluent → DIR → CUE bridge | 4 | 5 | 5 | 3 | 5 | Migration bridge |
| CEL only | 3 | 4 | 5 | 4 | 3 | Great for expressions (see KEP-2.16), weak alone for multi-resource templating |
| Starlark | 4 | 3 | 3 | 3 | 2 | Sandbox + new language |
| Rego/OPA | 3 | 4 | 3 | 3 | 2 | Policy-shaped, not templating |
| Jsonnet | 4 | 4 | 2 | 3 | 2 | Swap learning curve |
| KCL | 4 | 4 | 3 | 3 | 2 | Swap learning curve |
| Dhall | 3 | 5 | 2 | 3 | 2 | Niche |
| WASM evaluators | 4 | 4 | 3 | 3 | 2 | Supply-chain + ABI cost |
| OpenAPI + CEL | 3 | 4 | 5 | 4 | 3 | Schema+expr; needs templating IR |
| CDK8s / Pulumi style | 5 | 2–3 | 4 | 3 | 2 | Often imperative synthesis |

**Selected:** Go authoring + DIR + native evaluator + optional CUE bridge. Aligns with defkit DX lessons and KEP-2.16’s direction of shrinking CUE’s role per layer.

---

## 7. Prior art patterns

| System | Pattern | Takeaway |
|--------|---------|----------|
| Crossplane Composition Functions | Pipeline of functions; XRDs as schema | Authoring ≠ runtime function host |
| CDK8s | Code → Kubernetes manifests | Compile-time synthesis; not control-plane eval |
| Helm | Templates + values schema | Familiar but weak typing / composition |
| Kustomize | Declarative overlay | Patch model useful for traits |
| OPA/Rego | Policy-as-data | Good for admission, not resource synthesis |
| CEL in Kubernetes | Bounded expressions | Ideal for references/conditions; used in KEP-2.16 |
| Terraform providers | Schema + Go runtime | Plugin model; trust boundary explicit |
| defkit | Go → CUE | Compile-time only Go (security) |

Anti-pattern: executing arbitrary user Go inside the controller.

---

## 8. Developer experience (personas)

| Dimension | CUE | GEN_SDK | defkit | defkit+ToDefkit (proposed) |
|-----------|-----|---------|--------|------------------------|
| Concepts to learn | CUE language + KubeVela keywords | Go SDK + existing defs | Fluent API + occasional RawCUE | Fluent API + IR concepts (hidden) |
| Type safety | Weak at edit time | Strong for apps | Strong for defs | Strong for defs |
| IDE | CUE plugins uneven | Go | Go | Go |
| Runtime dependency | CUE/CueX | CUE | CUE | Native eval (CUE legacy optional) |
| Testability | cue vet / cluster | Go unit (apps) | Go unit (defs→CUE) | Go unit (defs→IR→resources) |
| Maintainer upgrade pain | High | Medium | High (still CUE) | Lower if IR stable |

---

## 9. Security analysis (authoring vs execution)

| Mode | Risk |
|------|------|
| Go at compile/CLI time (defkit) | Low: build-time only |
| Arbitrary Go in controller | High: RCE, non-determinism, supply chain |
| defkit schematic as data + allowlisted ops | Low–medium: must forbid unbounded I/O unless provider-gated |
| CueX providers today | High-trust definitions (already the model for SourceDefinition) |

The schematic document must remain **data**. Providers (HTTP, kube get) stay explicitly registered, matching today’s CueX trust model.

---

## 10. Open questions entering the PoC

1. How much of CueX provider surface must native defkit schematic cover before workflow parity is real?
2. Should health/status stay on CUE longer (expression-heavy) while render moves first?
3. Answered: defkit grows `ToDefkit()`; parallel dirkit rejected and deleted
4. Persistence: inline JSON in CR vs OCI artifact for large IRs?

---

## 11. Citations (primary)

- `apis/core.oam.dev/common/types.go` — Schematic
- `pkg/cue/definition/template.go` — AbstractEngine
- `pkg/appfile/{template,appfile,parser,validate}.go` — load/eval/validate
- `design/vela-cli/kep-defkit.md`, `pkg/definition/defkit/`
- `design/vela-cli/sdk_generating.md`, `pkg/definition/gen_sdk/`
- `design/vela-core/keps/2.16-source-definition/` — CEL migration for sources
- `go.mod` — cuelang.org/go v0.14.1
- Git history: `#6877`, CueX PRs, defkit `#7009`/`#7037`
