# Decision Record: CUE-Independent Definition Authoring (Defkit 2.0)

## Question

> Should KubeVela move toward a CUE-independent definition authoring architecture, and if so, what should the target architecture be?

## Answer

**Yes, gradually.** Target architecture (Defkit 2.0):

```text
defkit fluent API (existing NewComponent/Trait/Policy/…)
        |
        +-- ToCue() / ToYAML()           → schematic.cue      (compatibility)
        |
        +-- ToDefkit() / ToDefkitYAML()  → schematic.defkit   (native eval path)
                |
                v
         pkg/defschematic/eval (DefkitCategory)
                |
                v
         Kubernetes resources
```

Authors never use a second fluent API. Reusability stays: Go module + `Register` + goloader with `DEFKIT_EMIT=defkit`.

Do **not** pursue: flag-day CUE removal, GEN_SDK as authoring strategy, arbitrary Go execution in the control plane, or a parallel dirkit-style API.

## Why (evidence)

| Claim | Tag | Evidence |
|-------|-----|----------|
| CUE is multi-role runtime glue, not just syntax | Observed | `pkg/cue/definition/template.go`, CueX providers |
| defkit improves DX but historically left runtime CUE | Observed | `pkg/definition/defkit/cuegen.go`, kep-defkit Non-Goal #2 |
| GEN_SDK is the inverse problem | Observed | `design/vela-cli/sdk_generating.md` |
| Same fluent API can emit defkit schematic | Measured | `ToDefkit` / `defkitgen.go` + pocdefs |
| Native eval renders Component/Trait/Policy | Measured | Unit tests + k3d (VALIDATION.md) |
| Revision CRDs must know `schematic.defkit` | Measured | Nested prune → trait `field not found: output` |
| Workflow full cluster bypass unfinished | Observed | COMPATIBILITY Partial; stock `apply-component` |
| Authoring≠execution is the right seam | Inferred | Prior art (Crossplane functions, CDK compile-time, CEL) |

## Compared options

| Option | Decision |
|--------|----------|
| Continue CUE-only | Reject as sole path |
| Improve defkit CUE emit only | Necessary but insufficient |
| Parallel dirkit fluent API | Reject (dual stack); deleted in this PoC |
| Replace with Jsonnet/KCL/Dhall | Reject |
| Imperative Go plugins in-cluster | Reject |
| **Defkit fluent + schematic.defkit + CUE alternate** | **Accept** |

## Conditions for production investment

1. Wire defkit workflow steps into TaskRunner without CueX host.
2. Expand Expr/ops (foreach, health) with tests against stock definitions.
3. Keep `ToCue()` as alternate emit for mixed-mode (at least two major releases).
4. Measure render latency and binary size vs CueX on representative defs.
5. Treat nested `schematic.defkit` on ApplicationRevision/DefinitionRevision CRDs as a hard rollout requirement.

## PoC bottom line

Defkit 2.0 proves definitions are authored **only** with the existing defkit fluent API, cluster CRs use **`schematic.defkit`** from **`ToDefkitYAML()`**, and `ToCue()` remains the alternate emit. Runtime evaluation is native Go (`pkg/defschematic/eval`). CUE stays as a compatibility engine, not “delete CUE tomorrow.”

Historical note: an earlier spike used names DIR / dirkit / `schematic.dir`; those were renamed to defkit schematic in this re-wire.
