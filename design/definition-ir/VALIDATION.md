# Defkit 2.0 PoC — Live Validation

**Date:** 2026-08-12  
**Cluster:** k3d `dir-poc` (`KUBECONFIG=/tmp/dir-poc.kubeconfig`)  
**Image:** `vela-core:defkit-poc` (local build, `imagePullPolicy: Never`)  
**Base:** `17f55a794` / branch `poc/definition-ir`  
**Worktree:** `/Users/aykumar/oam/opensource/kubevela-dir-poc`

## Summary

Application `defkit-poc-app` reached **`running`**. Definitions were authored with the **defkit fluent API** and applied as **`schematic.defkit`** produced by **`ToDefkitYAML()`**. Component, Trait, and Policy paths rendered real cluster objects without CUE templates.

Full Atmos S3/EFS ports (`atmos-s3-v1`, `atmos-efs-v1`, `atmos-efs-volume-v1`) emit `schematic.defkit`, evaluate create/observe/replication/tags/claimName/validators in unit tests, and render claim Unstructureds on k3d against **fake claim CRDs** (no live AWS). Native health evaluates Crossplane Ready/Synced plus dynamic status fields.

Unit tests: `go test ./pkg/defschematic/... ./pkg/definition/defkit/... -count=1` pass.

## Defkit ToDefkit path

```text
pkg/defschematic/pocdefs (defkit.NewComponent/Trait/Policy/… + Atmos S3/EFS)
        → ToDefkitYAML()
        → hack/defkit-poc/examples/*.yaml (schematic.defkit)
        → controller DefkitCategory / pkg/defschematic/eval
        → Deployments, Services, ConfigMap, fake S3/EFS claims
```

`ToCue()` remains available as the alternate emit (unit-tested on webservice and atmos-s3-v1).

## Commands used

```bash
unset AWS_PROFILE
export KUBECONFIG=/tmp/dir-poc.kubeconfig

go run ./hack/defkit-poc/gen_manifests.go

docker build --build-arg TARGETARCH=$(go env GOARCH) \
  --build-arg VERSION=defkit-poc --build-arg GITVERSION=local \
  -t vela-core:defkit-poc .
k3d image import vela-core:defkit-poc --cluster dir-poc
kubectl -n vela-system set image deploy/vela-core vela-core=vela-core:defkit-poc
kubectl -n vela-system rollout restart deploy/vela-core

kubectl apply -f charts/vela-core/crds/core.oam.dev_componentdefinitions.yaml
kubectl apply -f charts/vela-core/crds/core.oam.dev_traitdefinitions.yaml
kubectl apply -f charts/vela-core/crds/core.oam.dev_policydefinitions.yaml
kubectl apply -f charts/vela-core/crds/core.oam.dev_definitionrevisions.yaml
kubectl apply -f charts/vela-core/crds/core.oam.dev_applicationrevisions.yaml

kubectl apply -f hack/defkit-poc/crds/
kubectl apply -f hack/defkit-poc/examples/defkit-webservice.yaml
kubectl apply -f hack/defkit-poc/examples/defkit-scaler.yaml
kubectl apply -f hack/defkit-poc/examples/defkit-override.yaml
kubectl apply -f hack/defkit-poc/examples/atmos-s3-v1.yaml
kubectl apply -f hack/defkit-poc/examples/atmos-efs-v1.yaml
kubectl apply -f hack/defkit-poc/examples/application.yaml
kubectl apply -f hack/defkit-poc/examples/application-atmos-claims.yaml

kubectl get app defkit-poc-app defkit-atmos-claims -o wide
kubectl get deploy,svc,cm,s3,efs -n default

# Synthetic Crossplane status for native health
kubectl patch s3 tenant-acme-logs -n default --subresource=status --type=merge -p \
  '{"status":{"conditions":[{"type":"Ready","status":"True"},{"type":"Synced","status":"True"}]}}'
kubectl patch efs tenant-acme-shared -n default --subresource=status --type=merge -p \
  '{"status":{"fileSystemId":"fs-demo","accessPointId":"fsap-demo","conditions":[{"type":"Ready","status":"True"},{"type":"Synced","status":"True"}]}}'
```

## Observed results

### Application (webservice PoC)

| Field | Value |
|-------|-------|
| `status.status` | `running` |
| Components | `frontend` (`defkit-webservice`), `backend` |
| Workflow | stock `apply-component` steps succeeded |

### Resources (webservice PoC)

| Object | Check |
|--------|-------|
| `deployment/frontend` | **replicas = 2** (trait `defkit-scaler`) |
| `deployment/backend` | Ready 1/1 |
| `service/frontend-svc`, `service/backend-svc` | Present |
| `configmap/show-override-defkit-policy` | `data.engine=defkit`, `data.components=frontend,backend` |

### Atmos claims (fake CRDs)

| Object | Check |
|--------|-------|
| `S3/tenant-acme-logs` | compositionRef `s3.objectstore.atmos.guidewire.com`, tags incl. `team=platform`, managementPolicies `*` |
| `EFS/tenant-acme-shared` | ClaimName `tenant-acme-shared`, tags incl. `env=dev` |
| Health before patch | `healthy=false`, messages `Bucket claim is not ready/synced.` / EFS not ready |
| Health after Ready+Synced (+ `fileSystemId=fs-demo`) | `bucket` / `filesystem` **healthy=true**; messages include `tenant-acme-logs` and `fs-demo` |

Unit coverage (no cluster required): create/observe, replication ConditionalStruct, tag SpreadIf, claimName truncate (>63), validators, `EvalHealth`.

### CRD requirement

Nested `schematic.defkit` on DefinitionRevision / ApplicationRevision CRDs is required. Without it, revisions prune the schematic and the controller falls back to the CUE path (`field not found: output` on traits). Same class of bug as the earlier DIR spike.

## Historical note

Earlier the same day, spike naming used DIR / `schematic.dir` / dirkit and app `dir-poc-app`. That path was re-wired to Defkit 2.0 (`ToDefkit` + `schematic.defkit`); dirkit was deleted. EFS `SetRawHeaderBlock` was replaced by first-class `Template.ClaimName`. Live AWS/Crossplane compositions remain out of scope.
