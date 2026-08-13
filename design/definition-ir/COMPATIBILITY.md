# Compatibility Matrix (Defkit 2.0 PoC)

Base: `17f55a794` (`poc/definition-ir`). Evidence from unit tests in `pkg/defschematic/eval` and live k3d cluster `defkit-poc` (see `VALIDATION.md`, 2026-08-12).

| Capability | Current KubeVela | Proposed PoC | Status | Notes |
| ---------- | ---------------- | ------------ | ------ | ----- |
| ComponentDefinition | ✓ | ✓ | Pass | Unit + cluster: `frontend`/`backend` Deployments + Services |
| TraitDefinition | ✓ | ✓ | Pass | Unit + cluster: `defkit-scaler` set `frontend` replicas to 2 |
| WorkflowStepDefinition | ✓ | ✓ | Partial | Native `EvalWorkflowStep` unit-tested; cluster used stock `apply-component` |
| PolicyDefinition | ✓ | ✓ | Pass | Cluster ConfigMap `show-override-defkit-policy` (`engine=defkit`) |
| Schema validation | ✓ (CUE) | ✓ (IR params) | Pass | `ir.ValidateParams` + appfile DefkitCategory path |
| Defaults | ✓ | ✓ | Pass | IR Param.Default (replicas/port) |
| Runtime values / context | ✓ | ✓ | Pass | `context.name` / `namespace` on live objects |
| References | ✓ | Partial | Partial | Param/context/input path refs; no full CUE unification |
| Pre-render | ✓ | Untested | Untested | Not in PoC scope |
| Post-render (traits) | ✓ | ✓ | Pass | Live trait patch measured |
| Workflow data passing | ✓ | Partial | Partial | Native step I/O in unit tests; not CueX-free TaskRunner |
| Conditions | ✓ | ✓ | Pass | And/Or/Not/Eq/Ne/IsSet/PathExists/Matches/Len (S3/EFS unit) |
| Iteration / foreach | ✓ | Fail | Fail | Not implemented in PoC IR |
| Composition (multi-resource) | ✓ | ✓ | Pass | Live Deployment + Service per component |
| CueX providers | ✓ | Fail | Fail | Explicit non-goal for PoC |
| Health / customStatus | ✓ | ✓ | Pass | Native Crossplane Ready/Synced + StatusField messages (S3/EFS unit; k3d status patch) |
| Plus / SpreadIf / ConditionalStruct | ✓ (CUE) | ✓ | Pass | Rich claim-style Definitions via ToDefkit + eval |
| ClaimName helper | n/a (RawCUE) | ✓ | Pass | md5 truncate for long DNS-1123 names |
| Nested / ConditionalParams / Validators | ✓ | ✓ | Pass | Create/observe branches; validator rejects |
| Fake claim CRDs (local proof) | n/a | ✓ | Pass | `hack/defkit-poc/crds` for offline claim kinds |
| OpenAPI ConfigMap | ✓ | ✓ | Pass | `openAPIFromDefkit` in capability controller |
| Mixed-mode CUE + defkit | ✓ | ✓ | Pass | Schematic branches; legacy CUE unchanged |
| IR→CUE bridge | n/a | ✓ | Pass | Best-effort `cuebridge.ToCUE` smoke |
| Revision CRD `schematic.defkit` | n/a | ✓ | Pass | Required; prune caused `workflowFailed` until CRDs applied |
| VelaQL / config templates | ✓ | Fail | Fail | Out of scope |
| Live cloud claim (Crossplane-backed object store) | ✓ | ✓ | Pass | EKS proof 2026-08-13: claim Ready/Synced; cloud bucket observed |

## Evidence tags

- **Pass**: automated test and/or cluster observation recorded
- **Partial**: subset works; gaps documented
- **Fail**: not implemented
- **Untested**: code path absent or stubbed
