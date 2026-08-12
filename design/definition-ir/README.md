# Defkit 2.0 PoC — Reproduce (`ToDefkit` + `schematic.defkit`)

## Identity

| Item | Value |
|------|-------|
| Base commit | `17f55a7940162a55603b22c5df10e2ddc15e50f0` (`origin/master`) |
| Worktree | `/Users/aykumar/oam/opensource/kubevela-dir-poc` |
| Branch | `poc/definition-ir` |

## Naming

| Surface | Name |
|---------|------|
| Schematic | `spec.schematic.defkit.template` |
| Category | `defkit` (`DefkitCategory`) |
| Document apiVersion | `defkit.oam.dev/v1alpha1` |
| Author APIs | `ToDefkit()` / `ToDefkitJSON()` / `ToDefkitYAML()` |
| CUE alternate | `ToCue()` / `ToYAML()` |
| Emit toggle | `DEFKIT_EMIT=cue\|defkit` |
| Runtime packages | `pkg/defschematic/{ir,eval,cuebridge}` |

Spike names DIR / dirkit / native / blueprint are obsolete.

## Unit tests

```bash
cd /Users/aykumar/oam/opensource/kubevela-dir-poc
unset KUBECONFIG AWS_PROFILE
go test ./pkg/defschematic/... ./pkg/definition/defkit/... -count=1
```

## Generate example manifests

```bash
go run ./hack/defkit-poc/gen_manifests.go ./hack/defkit-poc/examples
```

Manifests are produced by `ToDefkitYAML()` from fluent defkit authors in `pkg/defschematic/pocdefs`.

## Go module reuse

```bash
# cmd/register main calls defkit.ToJSON()
DEFKIT_EMIT=defkit go run ./cmd/register
# goloader LoadResult.Defkit is applied as schematic.defkit via FromDefkitTemplate
```

## Live k3d

`.vscode/scripts/k3d-*.sh` are **not** present on this `origin/master` tip. Follow Obsidian **KubeVela Test Environment Setup** / Dockerfile flow:

```bash
unset AWS_PROFILE
unset KUBECONFIG

K3D_CLUSTER_NAME=defkit-poc
K3D_CONFIG_FILE=/tmp/defkit-poc.kubeconfig
KUBEVELA_IMAGE=vela-core:defkit-poc
KUBEVELA_ARCH=$(go env GOARCH)

k3d cluster create "${K3D_CLUSTER_NAME}" \
  --image rancher/k3s:v1.31.5-k3s1 \
  --kubeconfig-update-default=false \
  --kubeconfig-switch-context=false \
  --wait

k3d kubeconfig write "${K3D_CLUSTER_NAME}" --output "${K3D_CONFIG_FILE}"

docker build \
  --build-arg TARGETARCH="${KUBEVELA_ARCH}" \
  --build-arg VERSION=defkit-poc \
  --build-arg GITVERSION=local \
  -t "${KUBEVELA_IMAGE}" .

k3d image import "${KUBEVELA_IMAGE}" --cluster "${K3D_CLUSTER_NAME}"

helm install vela-core ./charts/vela-core \
  --namespace vela-system --create-namespace \
  --kubeconfig "${K3D_CONFIG_FILE}" \
  --set image.repository=vela-core \
  --set image.tag=defkit-poc \
  --set image.pullPolicy=Never \
  --wait --timeout 8m

kubectl --kubeconfig "${K3D_CONFIG_FILE}" apply -f charts/vela-core/crds/
kubectl --kubeconfig "${K3D_CONFIG_FILE}" apply -f hack/defkit-poc/examples/defkit-webservice.yaml
kubectl --kubeconfig "${K3D_CONFIG_FILE}" apply -f hack/defkit-poc/examples/defkit-scaler.yaml
kubectl --kubeconfig "${K3D_CONFIG_FILE}" apply -f hack/defkit-poc/examples/defkit-override.yaml
kubectl --kubeconfig "${K3D_CONFIG_FILE}" apply -f hack/defkit-poc/examples/application.yaml
kubectl --kubeconfig "${K3D_CONFIG_FILE}" get app defkit-poc-app -w
```

If the Application fails with `field not found: output` on a trait, ensure ApplicationRevision/DefinitionRevision CRDs include nested `schematic.defkit`, then delete and recreate the Application (see VALIDATION.md).

## Results

See [VALIDATION.md](./VALIDATION.md). Prior DIR spike (`dir-poc-app`, 2026-08-12) proved the eval path; Defkit 2.0 re-wires authoring to `ToDefkit` + `schematic.defkit`.
