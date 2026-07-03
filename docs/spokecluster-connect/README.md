# SpokeCluster Connect (Phase 1)

A hub-side `SpokeCluster` custom resource and a dedicated `vela-cluster-core`
controller-manager that attaches KubeVela to existing Kubernetes clusters, reads
their state on demand, and surfaces them as first-class fleet objects
(`kubectl get spokeclusters`). This is Phase 1 (Connect mode) of the Cluster
Infrastructure KEP.

This document explains what was built, why it is shaped the way it is, how every
piece fits together, how to reproduce the whole thing on k3d with copy-paste
commands, and how to run it for real against AWS EKS using EKS Pod Identity.

---

## Table of contents

- [1. What this is and why](#1-what-this-is-and-why)
- [2. Architecture](#2-architecture)
- [3. The SpokeCluster API](#3-the-spokecluster-api)
- [4. How it works, end to end](#4-how-it-works-end-to-end)
- [5. Code map (file by file)](#5-code-map-file-by-file)
- [6. Reproduce on k3d (full walkthrough)](#6-reproduce-on-k3d-full-walkthrough)
- [7. Run it on AWS EKS with Pod Identity](#7-run-it-on-aws-eks-with-pod-identity)
- [8. CLI reference](#8-cli-reference)
- [9. Testing](#9-testing)
- [10. Troubleshooting](#10-troubleshooting)
- [11. Design decisions and FAQ](#11-design-decisions-and-faq)
- [12. What is next (Phase 2+)](#12-what-is-next-phase-2)

---

## 1. What this is and why

KubeVela's `Application` CRD assumes a cluster already exists and is reachable.
Multi-cluster today is a labeled Secret in `vela-system` that cluster-gateway
reads. That Secret is opaque: you cannot `kubectl get` your fleet, you cannot see
whether a spoke is reachable, what version it runs, or how many nodes it has, and
there is no controller keeping any of that fresh.

**Connect Phase 1 makes a managed cluster a first-class object.** You apply a
`SpokeCluster` on the hub, and a controller:

1. resolves a credential (a static kubeconfig, or AWS EKS via workload identity),
2. materializes the cluster-gateway Secret so the rest of KubeVela can reach the
   spoke,
3. probes the spoke on demand and records whether it is reachable,
4. discovers the spoke's Kubernetes version, node count, platform, and capacity,
5. reports all of that on the `SpokeCluster` status, refreshed continuously.

Two principles shape the design, both inherited from the KEP:

- **The hub reads; the spoke never pushes.** All state flows into the hub by the
  hub pulling on demand. A spoke has no agent and reports nothing upward. Hub
  downtime cannot corrupt spoke state, and a spoke cannot corrupt the hub fleet
  registry.
- **`SpokeCluster` is a hub-role object.** It is the hub's handle for one managed
  cluster. In later phases the same object dispatches a `ClusterBlueprint` to the
  spoke; in Phase 1 it only connects and observes.

Everything ships behind a feature gate (`EnableSpokeClusterCRD`) that defaults
**off**, so installing the chart without opting in changes nothing.

### Scope

| In scope (Phase 1) | Out of scope (later phases) |
| --- | --- |
| `SpokeCluster` CRD, `mode: connect` only | `provision` and `adopt` modes |
| Pluggable credentials: kubeconfig + AWS EKS | Spoke-side self-reconciling `Cluster` |
| Connect, probe, discovery, status, conditions | Blueprint dispatch to the spoke |
| cluster-gateway registration and detach | `ClusterPlane`, `ClusterBlueprint`, rollouts |
| `kubectl get spokeclusters`, `vela cluster spokes` | Drift detection, cross-cluster inputs |
| A separate `vela-cluster-core` manager pod | Multi-hub, federation |

---

## 2. Architecture

### 2.1 Two managers, one chart

vela-core is left untouched. Cluster-infrastructure resources are reconciled by a
**separate `vela-cluster-core` manager** that runs as its own pod alongside
vela-core in the same namespace. Both are installed by the one `vela-core` Helm
chart; the cluster-core pod only appears when `featureGates.enableSpokeClusterCRD`
is true.

```
                         HUB CLUSTER (vela-system namespace)
  +-------------------------------------------------------------------------+
  |                                                                         |
  |   vela-core (Deployment)              vela-cluster-core (Deployment)    |
  |   - Application controller            - SpokeCluster controller         |
  |   - X-Definition controllers          - SpokeCluster webhooks (opt-in)  |
  |   - does NOT see SpokeCluster         - reads spokes on demand          |
  |                                                                         |
  |   cluster-gateway (Deployment)                                          |
  |   - aggregated API server, proxies hub -> spoke Kubernetes API          |
  |   - reads the labeled Secret the SpokeCluster controller materializes   |
  |                                                                         |
  +---------------------------------|---------------------------------------+
                                    |  hub-initiated pull (probe, discovery,
                                    |  read-through) through cluster-gateway
                                    v
                         +---------------------+
                         |    SPOKE CLUSTER    |
                         |  - no agent         |
                         |  - pushes nothing   |
                         +---------------------+
```

Why a separate pod rather than folding the controller into vela-core:

- **Blast-radius isolation.** A bug in cluster-infrastructure reconciliation
  cannot take down application delivery, and vice versa.
- **Independent scaling and lifecycle.** The fleet controller can be scaled,
  restarted, or gated independently.
- **It matches the KEP.** The KEP calls for a `vela-cluster-core` engine. This is
  that engine, starting with its first controller.

### 2.2 Reconcile flow

```
   User / GitOps applies a SpokeCluster on the hub
                 |
                 v
   SpokeClusterController (in vela-cluster-core)
                 |
   1. Resolve credential  -----> CredentialProvider (kubeconfig | aws)
                 |                 returns endpoint, CA, token or client cert,
                 |                 and a NextRefresh time
                 v
   2. Materialize gateway Secret (ownerRef'd to the SpokeCluster)
                 |                 label cluster.core.oam.dev/cluster-credential-type
                 v
   3. Probe /healthz through cluster-gateway (hub-initiated pull)
                 |
                 v
   4. Discover version + nodes + platform through cluster-gateway
                 |
                 v
   5. Write status: connection, clusterInfo, conditions, lastProbeTime
                 |
                 v
   Requeue after min(probeInterval, time-until-credential-refresh)
```

The controller never contacts the spoke except through cluster-gateway, and it
only writes hub objects. That keeps the "hub reads, spoke never pushes" boundary
intact.

### 2.3 How credentials become connectivity

The controller does not talk to the spoke API directly with the raw credential.
It translates whatever the user declared into the one shape cluster-gateway
understands: a labeled Secret in `vela-system` named after the cluster, holding
`endpoint`, `ca.crt`, and either a `token` or a `tls.crt`/`tls.key` pair. This is
exactly what `vela cluster join` produces, so every existing consumer
(read-through, topology dispatch, `vela cluster list`) treats a SpokeCluster
spoke identically to a hand-joined one.

```
  kubeconfig arm:  read source Secret -> parse kubeconfig -> endpoint/CA/token-or-cert
  aws arm:         AssumeRole(per-cluster role) -> eks:DescribeCluster (endpoint+CA)
                   -> presign STS GetCallerIdentity -> k8s-aws-v1. bearer token
                   -> refresh ~1 minute before the 15-minute presign window closes
                          |
                          v
              materialized cluster-gateway Secret
```

---

## 3. The SpokeCluster API

`SpokeCluster` is namespaced (`core.oam.dev/v1beta1`), conventionally in
`vela-system`. The kind is `SpokeCluster` (not `Cluster`) to reserve `Cluster`
for the spoke-side self-reconciling object in later phases and to avoid colliding
with the Cluster API `clusters.cluster.x-k8s.io`.

### 3.1 Spec

```yaml
apiVersion: core.oam.dev/v1beta1
kind: SpokeCluster
metadata:
  name: prod-us-east-1
  namespace: vela-system
spec:
  # connect attaches to a cluster that already exists and never creates one.
  # Phase 1 accepts connect only; provision and adopt are rejected by validation.
  mode: connect

  # Discriminated union keyed by the auth method. Exactly one arm is set and it
  # must match type.
  credential:
    type: kubeconfig            # kubeconfig | aws
    kubeconfig:
      secretRef:
        name: prod-kubeconfig   # Secret holding a kubeconfig
        namespace: vela-system  # optional, defaults to the SpokeCluster namespace
        key: kubeconfig         # optional, defaults to "kubeconfig"
    # aws:
    #   authMode: podIdentity   # podIdentity | irsa
    #   clusterName: prod-us-east-1
    #   region: us-east-1
    #   roleArn: arn:aws:iam::<account>:role/<per-cluster-scoped-role>
    #   externalId: <optional confused-deputy mitigation>

  probeIntervalSeconds: 30      # default 30, min 10, max 600
  probeTimeoutSeconds: 10       # default 10, min 1, max 120
  deletionPolicy: detach        # detach (default) removes connectivity; orphan leaves it

  # Phase 2 stub fields. The webhook rejects them in connect mode today.
  # blueprintRef: { name, revision }
  # rolloutStrategyRef: { name }
```

### 3.2 Status

Written only by the controller. The spoke never pushes any of this.

```yaml
status:
  connection: Connected         # Connected | Disconnected | Unknown
  conditions:                   # standard metav1.Condition entries
    - type: CredentialValid     # source credential resolved
    - type: Registered          # cluster-gateway Secret materialized
    - type: Connected           # probe reached the spoke
    - type: InfoSynced          # discovery populated clusterInfo
  clusterInfo:
    kubernetesVersion: v1.31.5+k3s1
    platform: k3s               # inferred from node labels: eks | gke | aks | kind | k3s
    region: us-east-1           # from the aws arm or topology.kubernetes.io/region
    nodeCount: 3
    totalCPU: "12"
    totalMemory: 32809156Ki
    apiServerEndpoint: https://XXXX.eks.amazonaws.com
    latencyMillis: 4
  lastProbeTime: "2026-07-03T04:45:00Z"
  observedGeneration: 1
```

### 3.3 Printer columns

```
$ kubectl get spokeclusters
NAME             MODE      VERSION        NODES   PLATFORM   STATUS      AGE
prod-us-east-1   connect   v1.31.5+k3s1   3       k3s        Connected   45s

$ kubectl get spokeclusters -o wide
# adds REGION, ENDPOINT, CPU, MEMORY, LATENCY, AUTH, LAST PROBE
```

### 3.4 Conditions

| Condition | True when |
| --- | --- |
| `CredentialValid` | the credential provider resolved the source credential |
| `Registered` | the cluster-gateway Secret was written |
| `Connected` | the on-demand `/healthz` probe reached the spoke |
| `InfoSynced` | discovery populated `status.clusterInfo` |

A probe failure sets `connection: Disconnected` and `Connected=False` while
leaving `Registered` and `CredentialValid` true, so you can tell a credential
problem apart from a reachability problem.

---

## 4. How it works, end to end

This section traces one reconcile in detail.

### Step 1: credential resolution

The controller looks up a `CredentialProvider` by `spec.credential.type` and
calls `Materialize`, which returns a `Materialized`:

```go
type Materialized struct {
    Endpoint       string    // spoke API server URL
    CAData         []byte    // PEM CA bundle; empty means skip TLS verify
    Token          string    // bearer token (aws arm, or a token kubeconfig)
    ClientCertData []byte    // mTLS arm (x509 kubeconfig)
    ClientKeyData  []byte
    Region         string    // aws arm, surfaced into clusterInfo
    NextRefresh    time.Time // zero for static; future time for aws
}
```

- **kubeconfig provider**: reads the referenced Secret, parses the kubeconfig with
  client-go `clientcmd`, and pulls the current context's endpoint, CA, and auth.
  A token kubeconfig yields `Token`; an x509 kubeconfig yields the cert/key pair.
  `exec`-based kubeconfigs are rejected (connect needs a static credential).
  `NextRefresh` is zero, so no refresh is scheduled.
- **aws provider**: assumes the per-cluster role, describes the EKS cluster for
  its endpoint and CA, then mints an EKS bearer token (see [section 7](#7-run-it-on-aws-eks-with-pod-identity)).
  `NextRefresh` is set ~1 minute before the token's 15-minute presign window
  closes.

On success the controller sets `CredentialValid=True`. On failure it records the
error, sets `CredentialValid=False`, and requeues.

### Step 2: materialize the cluster-gateway Secret

`register` (in `connect.go`) upserts a Secret named after the SpokeCluster in
`vela-system`, shaped exactly like what `vela cluster join` writes:

- label `cluster.core.oam.dev/cluster-credential-type` = `ServiceAccountToken`
  (token) or `X509Certificate` (cert),
- data keys `endpoint`, `ca.crt`, and either `token` or `tls.crt`/`tls.key`.

Under the default `detach` policy the Secret gets an owner reference to the
SpokeCluster, so Kubernetes garbage-collects it when the SpokeCluster is deleted.
Under `orphan` the owner reference is omitted so the Secret survives. On success
the controller sets `Registered=True`.

### Step 3: probe (hub-initiated pull)

`probe` (in `probe.go`) calls `multicluster.RequestRawK8sAPIForCluster(ctx,
"healthz", name, cfg)`, which routes through the cluster-gateway proxy
(`/apis/cluster.core.oam.dev/v1alpha1/clustergateways/<name>/proxy/healthz`). It
measures round-trip latency and honours `probeTimeoutSeconds`. Reachable sets
`connection: Connected` and `Connected=True`; a failure sets `Disconnected` and
`Connected=False` and skips discovery for this pass.

### Step 4: discovery

`discover` (in `discovery.go`) reuses two existing multicluster helpers through
the gateway: `GetVersionInfoFromCluster` for the server version and
`GetClusterInfo` for node counts and capacity. Platform is inferred from node
labels (`eks.amazonaws.com/*` -> eks, `cloud.google.com/gke-*` -> gke,
`kubernetes.azure.com/*` -> aks, k3s OS image -> k3s, `kind` in the node name ->
kind). Region comes from the aws arm or `topology.kubernetes.io/region`. On
success the controller sets `InfoSynced=True` and fills `status.clusterInfo`.

### Step 5: status write and requeue

Status is written with conflict retry. The requeue interval is
`min(probeIntervalSeconds, time-until-NextRefresh)` with a small floor, so an
AWS spoke is reconciled again before its token expires, and a kubeconfig spoke is
reconciled on the plain probe cadence.

### Deletion

A finalizer (`spokecluster.core.oam.dev/finalizer`) guards deletion. Under
`detach` the controller calls `multicluster.DetachCluster` (which scrubs
ResourceTracker references and deletes the gateway Secret), then releases the
finalizer. Under `orphan` it releases the finalizer and leaves the Secret. If a
spoke was never fully registered, deletion still completes (a missing Secret is
tolerated).

---

## 5. Code map (file by file)

Everything lives behind the `EnableSpokeClusterCRD` gate. Library code
(controller, providers) is binary-agnostic and is wired into the separate
`vela-cluster-core` manager.

### API types

| File | What it contains |
| --- | --- |
| `apis/core.oam.dev/v1beta1/spokecluster_types.go` | `SpokeCluster`/`SpokeClusterList`, the discriminated credential union, enums (`SpokeClusterMode`, `CredentialType`, `AWSAuthMode`, `SpokeDeletionPolicy`, `ConnectionState`), condition constants, kubebuilder markers, printer columns |
| `apis/core.oam.dev/v1beta1/register.go` | `SpokeCluster` type metadata + `SchemeBuilder.Register` |
| `apis/core.oam.dev/v1beta1/zz_generated.deepcopy.go` | generated deepcopy (via `make manifests`) |
| `charts/vela-core/crds/core.oam.dev_spokeclusters.yaml` | generated CRD YAML, shipped in the chart |

### Feature gate

| File | What it contains |
| --- | --- |
| `pkg/features/controller_features.go` | `EnableSpokeClusterCRD` (default false, Alpha) |

### Credential providers

| File | What it contains |
| --- | --- |
| `pkg/spokecluster/credential/provider.go` | `Provider` interface, `Materialized`, `Registry`, `DefaultRegistry()` |
| `pkg/spokecluster/credential/kubeconfig.go` | static kubeconfig provider |
| `pkg/spokecluster/credential/aws.go` | AWS provider (AssumeRole -> DescribeCluster -> token), SDK calls behind `eksDescribeAPI`/`awsClientFactory` seams |
| `pkg/spokecluster/credential/aws_token.go` | EKS `k8s-aws-v1.` presigned-STS token generator behind `stsPresignAPI` |
| `pkg/spokecluster/credential/*_test.go` | table tests with fake STS/EKS clients; no live AWS |

### Controller

| File | What it contains |
| --- | --- |
| `pkg/controller/core.oam.dev/v1beta1/spokecluster/spokecluster_controller.go` | `Reconciler`, reconcile loop, `SetupWithManager`, gated `Setup`, requeue math, status/condition helpers |
| `.../spokecluster/connect.go` | materialize the gateway Secret (ownerRef under detach), delete helper |
| `.../spokecluster/probe.go` | hub-initiated `/healthz` probe with latency |
| `.../spokecluster/discovery.go` | version + node discovery, platform/region inference |
| `.../spokecluster/deletion.go` | finalizer cleanup, detach vs orphan |
| `.../spokecluster/*_test.go` | fake-client + mock-provider reconcile and deletion tests |
| `pkg/controller/core.oam.dev/v1beta1/setup.go` | note that SpokeCluster is NOT registered in vela-core |

### Webhook

| File | What it contains |
| --- | --- |
| `pkg/webhook/core.oam.dev/v1beta1/spokecluster/validation.go` | pure `Validate`/`Default` (connect-only, credential union, aws required fields, Phase 2 stub rejection) |
| `.../spokecluster/validating_handler.go` | validating admission handler + register |
| `.../spokecluster/mutating_handler.go` | defaulting admission handler + register |
| `.../spokecluster/validation_test.go` | table tests for accept/reject/default |

### The vela-cluster-core binary

| File | What it contains |
| --- | --- |
| `cmd/cluster-core/main.go` | entrypoint |
| `cmd/cluster-core/app/server.go` | flags, manager (uses `common.Scheme`), cluster-gateway init, controller + webhook registration, webhook cert wait |

### Packaging

| File | What it contains |
| --- | --- |
| `charts/vela-core/templates/cluster-core/vela-cluster-core.yaml` | the gated Deployment, optional AWS SA + rolebinding |
| `charts/vela-core/values.yaml` | `featureGates.enableSpokeClusterCRD` and the `clusterCore` block |
| `Dockerfile` | builds and ships both `manager` and `cluster-manager` |
| `Makefile` | `cluster-manager` build target |
| `.vscode/scripts/k3d-setup.sh` | `ENABLE_SPOKE_CLUSTER=true` opt-in |

### CLI and tests

| File | What it contains |
| --- | --- |
| `references/cli/spokecluster.go` | `vela cluster spokes list/show` |
| `references/cli/cluster.go` | registers the `spokes` group |
| `apis/core.oam.dev/v1beta1/spokecluster_admission_test.go` | envtest admission suite (skips without `KUBEBUILDER_ASSETS`) |
| `test/e2e-multicluster-test/spokecluster_test.go` | CI e2e, self-skips when the CRD is absent |

---

## 6. Reproduce on k3d (full walkthrough)

This is the exact flow used to verify the prototype: a hub and a spoke as two
k3d clusters, vela-core plus vela-cluster-core installed on the hub with the
feature enabled, and a SpokeCluster connecting the hub to the spoke.

### 6.1 Prerequisites

- Docker, `k3d` >= 5.7, `kubectl` >= 1.28, `helm` >= 3.14, Go matching `go.mod`.
- Run from the repo root: `/workspaces/oam/opensource/kubevela`.
- In this devcontainer, always clear two environment variables first:

```bash
unset AWS_PROFILE   # the container ships AWS_PROFILE="" which breaks kubectl exec auth
unset KUBECONFIG    # the host KUBECONFIG points at /Users/... paths absent here
```

> **Pin k3s to v1.31.5.** cluster-gateway's aggregated discovery breaks on k8s
> 1.35 with `unable to parse bytes as PEM block`. Every `k3d cluster create`
> below pins `rancher/k3s:v1.31.5-k3s1`.

### 6.2 Create hub and spoke on a shared network

The hub's gateway pod must reach the spoke API server, and this container must
reach both, so all three share one Docker network. Each cluster's serverlb name
is added as a TLS SAN.

```bash
docker network create sc-poc

k3d cluster create sc-hub \
  --image rancher/k3s:v1.31.5-k3s1 --network sc-poc --api-port 6560 \
  --k3s-arg=--tls-san=k3d-sc-hub-serverlb@server:0 --wait

k3d cluster create sc-spoke \
  --image rancher/k3s:v1.31.5-k3s1 --network sc-poc --api-port 6561 \
  --k3s-arg=--tls-san=k3d-sc-spoke-serverlb@server:0 --wait

# Attach this devcontainer to the shared network so kubectl/helm can reach both.
docker network connect sc-poc "$(hostname)" || true
```

Wait until each node registers (a fresh cluster reports "created" a few seconds
before the node is `Ready`):

```bash
docker exec k3d-sc-hub-server-0   kubectl get nodes
docker exec k3d-sc-spoke-server-0 kubectl get nodes
```

### 6.3 Build the image (both binaries) and import into the hub

The Dockerfile builds `manager` (vela-core) and `cluster-manager`
(vela-cluster-core) into one image. For a quick local loop you can build the two
binaries directly and assemble a small image:

```bash
mkdir -p /tmp/scimg
CGO_ENABLED=0 GOOS=linux GOARCH="$(go env GOARCH)" \
  go build -o /tmp/scimg/manager -ldflags "-s -w -X github.com/oam-dev/kubevela/version.VelaVersion=dev" ./cmd/core/main.go
CGO_ENABLED=0 GOOS=linux GOARCH="$(go env GOARCH)" \
  go build -o /tmp/scimg/cluster-manager -ldflags "-s -w -X github.com/oam-dev/kubevela/version.VelaVersion=dev" ./cmd/cluster-core/main.go
cp entrypoint.sh /tmp/scimg/

cat > /tmp/scimg/Dockerfile <<'DOCKERFILE'
FROM alpine:3.18
RUN apk add --no-cache ca-certificates bash expat
WORKDIR /
COPY manager /usr/local/bin/manager
COPY cluster-manager /usr/local/bin/cluster-manager
COPY entrypoint.sh /usr/local/bin/
ENTRYPOINT ["entrypoint.sh"]
CMD ["manager"]
DOCKERFILE

docker build -t vela-core:local /tmp/scimg
k3d image import vela-core:local -c sc-hub
```

Alternatively, `make cluster-manager` builds `bin/cluster-manager`, and the
standard image build is `docker build -t vela-core:local .` from the repo root
(it now produces both binaries).

### 6.4 Point kubectl at the hub

k3d writes a kubeconfig with `server: https://0.0.0.0:<port>`, which is not
reachable from inside the container. Repoint it at the hub's `server-0` container
IP on the shared network (that IP is in the cert SAN; the serverlb IP is not).

```bash
k3d kubeconfig merge sc-hub --output /tmp/sc-hub.kubeconfig
HUB_IP=$(docker inspect k3d-sc-hub-server-0 \
  --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' | head -1)
KUBECONFIG=/tmp/sc-hub.kubeconfig kubectl config set-cluster k3d-sc-hub \
  --server="https://${HUB_IP}:6443"

export KUBECONFIG=/tmp/sc-hub.kubeconfig
kubectl get nodes   # k3d-sc-hub-server-0  Ready
```

### 6.5 Install vela-core + vela-cluster-core with the feature on

```bash
helm install vela-core ./charts/vela-core \
  --namespace vela-system --create-namespace \
  --set image.repository=vela-core --set image.tag=local --set image.pullPolicy=Never \
  --set controllerArgs.reSyncPeriod=1m \
  --set featureGates.enableSpokeClusterCRD=true \
  --wait --timeout 4m
```

Verify both managers and the CRD:

```bash
kubectl get pods -n vela-system
# vela-core-...                Running
# vela-core-cluster-core-...   Running   <- the vela-cluster-core pod
# vela-core-cluster-gateway-.. Running

kubectl get crd spokeclusters.core.oam.dev
```

The k3d helper script wraps steps 6.5 with an opt-in flag:

```bash
ENABLE_SPOKE_CLUSTER=true .vscode/scripts/k3d-setup.sh --cluster sc-hub
```

### 6.6 Create the spoke credential Secret on the hub

Build a spoke kubeconfig whose server is the spoke's `server-0` IP on the shared
network (reachable from the hub's gateway pod), and store it as a Secret on the
hub:

```bash
k3d kubeconfig merge sc-spoke --output /tmp/sc-spoke.kubeconfig
SPOKE_IP=$(docker inspect k3d-sc-spoke-server-0 \
  --format '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' | head -1)
KUBECONFIG=/tmp/sc-spoke.kubeconfig kubectl config set-cluster k3d-sc-spoke \
  --server="https://${SPOKE_IP}:6443"

kubectl create secret generic sc-spoke-kubeconfig -n vela-system \
  --from-file=kubeconfig=/tmp/sc-spoke.kubeconfig
```

### 6.7 Apply the SpokeCluster and watch it connect

```bash
kubectl apply -f - <<'YAML'
apiVersion: core.oam.dev/v1beta1
kind: SpokeCluster
metadata:
  name: sc-spoke
  namespace: vela-system
spec:
  mode: connect
  credential:
    type: kubeconfig
    kubeconfig:
      secretRef:
        name: sc-spoke-kubeconfig
        namespace: vela-system
YAML

# Within ~15s:
kubectl get spokeclusters -n vela-system -o wide
```

Expected:

```
NAME       MODE      VERSION        NODES   PLATFORM   STATUS      ...   ENDPOINT                  ... AUTH         ...
sc-spoke   connect   v1.31.5+k3s1   1       k3s        Connected   ...   https://172.31.0.4:6443   ... kubeconfig   ...
```

### 6.8 Verify conditions, the gateway Secret, and read-through

```bash
# All four conditions True
kubectl get spokecluster sc-spoke -n vela-system \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status} ({.reason}){"\n"}{end}'
# CredentialValid=True (Materialized)
# Registered=True (SecretMaterialized)
# Connected=True (ProbeSucceeded)
# InfoSynced=True (DiscoveryOK)

# The materialized gateway Secret carries the credential-type label and an ownerRef
kubectl get secret sc-spoke -n vela-system \
  -o jsonpath='label={.metadata.labels.cluster\.core\.oam\.dev/cluster-credential-type} owner={.metadata.ownerReferences[0].kind}/{.metadata.ownerReferences[0].name}{"\n"}'
# label=X509Certificate owner=SpokeCluster/sc-spoke

# Read-through: list namespaces on the spoke, through the hub gateway proxy
kubectl get --raw \
  /apis/cluster.core.oam.dev/v1alpha1/clustergateways/sc-spoke/proxy/api/v1/namespaces \
  | head -c 300
```

### 6.9 Try the CLI

```bash
# build once
go build -o /tmp/vela ./references/cmd/cli

KUBECONFIG=/tmp/sc-hub.kubeconfig /tmp/vela cluster spokes list
KUBECONFIG=/tmp/sc-hub.kubeconfig /tmp/vela cluster spokes show sc-spoke
```

### 6.10 Delete and confirm detach

```bash
kubectl delete spokecluster sc-spoke -n vela-system
# detach policy removes the gateway Secret:
kubectl get secret sc-spoke -n vela-system   # NotFound
```

### 6.11 Tear down

```bash
k3d cluster delete sc-hub sc-spoke
docker network disconnect sc-poc "$(hostname)" 2>/dev/null || true
docker network rm sc-poc 2>/dev/null || true
```

---

## 7. Run it on AWS EKS with Pod Identity

This section is the in-depth guide to connecting the hub to real EKS spokes using
**EKS Pod Identity**, with **IRSA** as an alternative. It covers the identity
model, the IAM roles, the cross-account chaining, the Kubernetes access entries,
and the SpokeCluster manifests. No static credentials are stored anywhere.

### 7.1 What the AWS credential provider does

When `spec.credential.type: aws`, the controller (running in the
vela-cluster-core pod) does the following on every reconcile:

1. **Load the base identity.** The vela-cluster-core pod runs with a base AWS
   identity provided by EKS Pod Identity (or IRSA). This is the pod's own
   identity on the hub cluster; it holds no per-spoke permissions itself.
2. **Assume the per-cluster role.** Using STS, it assumes `spec.credential.aws.roleArn`
   (optionally with `externalId`). That role is scoped to exactly one spoke
   cluster. This is the single hop that turns the hub's base identity into a
   spoke-scoped identity.
3. **Describe the cluster.** With the assumed role it calls `eks:DescribeCluster`
   for the spoke's API server endpoint and CA certificate.
4. **Mint an EKS bearer token.** It presigns an STS `GetCallerIdentity` request
   bound to the cluster name via the `x-k8s-aws-id` header, then base64url-encodes
   it with the `k8s-aws-v1.` prefix. This is byte-for-byte what
   `aws eks get-token` produces. EKS validates the token by replaying the
   presigned URL and checking the identity and cluster-id header.
5. **Materialize and refresh.** The endpoint, CA, and token become the
   cluster-gateway Secret. Because the token's presign window is 15 minutes, the
   controller schedules the next reconcile ~1 minute early so the token never
   goes stale mid-use.

The important property: **one credential can reach exactly one cluster.** The
per-cluster role only permits `eks:DescribeCluster` on its own cluster ARN, and
the Kubernetes access entry grants the assumed role read access on only that
cluster. Compromising the hub's base identity does not grant fleet-wide reach.

### 7.2 The identity model (single account first)

```
  vela-cluster-core pod (hub EKS)
     | Pod Identity association: SA -> hub base role
     v
  hub base role  (776...:role/oam-hub-base)
     | sts:AssumeRole   (allow-list of per-cluster role ARNs, never *)
     v
  per-cluster role  (776...:role/oam-spoke-prod-scoped)
     | eks:DescribeCluster on the ONE cluster ARN
     | + Kubernetes access entry on that cluster (AmazonEKSViewPolicy)
     v
  spoke EKS API server  (read-through via cluster-gateway)
```

### 7.3 The identity model (cross-account)

For a spoke in a different AWS account, the per-cluster role lives in the spoke's
account and trusts the hub base role. This is IAM role chaining brokered by STS:

```
  hub base role (account A: 776...)
     | sts:AssumeRole into account B, scoped to the one target ARN,
     | with an externalId of region/hubAccount/hubCluster/namespace/serviceAccount
     v
  per-cluster target role (account B: 627...:role/oam-spoke-dev-scoped)
     | trust: principal = hub base role, condition sts:ExternalId = <the above>
     | perms: eks:DescribeCluster on the account-B cluster ARN
     | + access entry on the account-B cluster
     v
  spoke EKS in account B
```

`externalId` is the confused-deputy mitigation: the target role can only be
assumed by the exact hub identity that the externalId encodes. Set it on the
SpokeCluster's `credential.aws.externalId` and match it in the target role's
trust policy.

### 7.4 Prerequisites

- A hub EKS cluster running vela-core + vela-cluster-core (this feature) with
  `authentication.enabled=true` (so the SA gets the manager ClusterRole) or the
  default cluster-admin binding.
- The **EKS Pod Identity Agent** addon installed on the hub cluster:

```bash
aws eks create-addon --cluster-name <hub-cluster> --region <hub-region> \
  --addon-name eks-pod-identity-agent
aws eks describe-addon --cluster-name <hub-cluster> --region <hub-region> \
  --addon-name eks-pod-identity-agent --query 'addon.status'   # wait for ACTIVE
```

- Each spoke EKS cluster must use an `authenticationMode` of `API` or
  `API_AND_CONFIG_MAP` (Pod Identity access entries require the API mode). Check
  and, if needed, update it (this change is additive but irreversible, so confirm
  with the cluster owner):

```bash
aws eks describe-cluster --name <spoke> --region <region> \
  --query 'cluster.accessConfig.authenticationMode'
# If CONFIG_MAP only:
aws eks update-cluster-config --name <spoke> --region <region> \
  --access-config authenticationMode=API_AND_CONFIG_MAP
```

### 7.5 IAM setup, step by step

The examples use placeholder account ids: hub `776719623202`, spoke
`627188849628`, region `us-east-1`, spoke cluster `prod-us-east-1`.

**Step 1: hub base role and its trust for Pod Identity.**

`hub-base-trust.json`:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": { "Service": "pods.eks.amazonaws.com" },
      "Action": ["sts:AssumeRole", "sts:TagSession"]
    }
  ]
}
```

`hub-base-perms.json` (assume only the per-cluster target roles it manages, never `*`):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "AssumeSpokeRoles",
      "Effect": "Allow",
      "Action": ["sts:AssumeRole", "sts:TagSession"],
      "Resource": [
        "arn:aws:iam::627188849628:role/oam-spoke-prod-scoped"
      ]
    }
  ]
}
```

```bash
aws iam create-role --role-name oam-hub-base \
  --assume-role-policy-document file://hub-base-trust.json
aws iam put-role-policy --role-name oam-hub-base \
  --policy-name assume-spoke-roles --policy-document file://hub-base-perms.json
```

**Step 2: per-cluster scoped role in the spoke account.**

`spoke-scoped-trust.json` (trust the hub base role, bound by externalId):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Principal": { "AWS": "arn:aws:iam::776719623202:role/oam-hub-base" },
      "Action": "sts:AssumeRole",
      "Condition": {
        "StringEquals": {
          "sts:ExternalId": "us-east-1/776719623202/<hub-cluster>/vela-system/vela-core-cluster-core"
        }
      }
    },
    {
      "Effect": "Allow",
      "Principal": { "AWS": "arn:aws:iam::776719623202:role/oam-hub-base" },
      "Action": "sts:TagSession"
    }
  ]
}
```

`spoke-scoped-perms.json` (describe only the one cluster):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "DescribeOneCluster",
      "Effect": "Allow",
      "Action": "eks:DescribeCluster",
      "Resource": "arn:aws:eks:us-east-1:627188849628:cluster/prod-us-east-1"
    }
  ]
}
```

```bash
# In the spoke account (627...):
aws iam create-role --role-name oam-spoke-prod-scoped \
  --assume-role-policy-document file://spoke-scoped-trust.json
aws iam put-role-policy --role-name oam-spoke-prod-scoped \
  --policy-name describe-prod --policy-document file://spoke-scoped-perms.json
```

> `eks:ListClusters` cannot be scoped to a single cluster ARN, so it is not
> granted here. Connect only needs `eks:DescribeCluster` on the one cluster.

**Step 3: Kubernetes access entry on the spoke cluster.** This is what actually
authorizes the assumed role inside the spoke's Kubernetes RBAC. Bind it to a
read policy for connect (probe + discovery only need read):

```bash
# In the spoke account (627...):
aws eks create-access-entry --cluster-name prod-us-east-1 --region us-east-1 \
  --principal-arn arn:aws:iam::627188849628:role/oam-spoke-prod-scoped

aws eks associate-access-policy --cluster-name prod-us-east-1 --region us-east-1 \
  --principal-arn arn:aws:iam::627188849628:role/oam-spoke-prod-scoped \
  --access-scope type=cluster \
  --policy-arn arn:aws:eks::aws:cluster-access-policy/AmazonEKSViewPolicy
```

> `AmazonEKSViewPolicy` is enough for Phase 1 (read-only probe and discovery). A
> later phase that dispatches workloads would need a broader policy or a custom
> access scope.

**Step 4: associate the hub SA with the base role via Pod Identity.** The
vela-cluster-core pod uses the SA created by the chart. With
`clusterCore.aws.serviceAccountRoleArn` set (below), the chart creates a
dedicated SA named `<release>-cluster-core`; otherwise it reuses the vela-core
SA. Associate whichever SA the pod runs as:

```bash
# In the hub account (776...), against the hub cluster:
aws eks create-pod-identity-association --region <hub-region> \
  --cluster-name <hub-cluster> \
  --namespace vela-system \
  --service-account vela-core-cluster-core \
  --role-arn arn:aws:iam::776719623202:role/oam-hub-base
```

The externalId EKS generates for the chained assume is
`<region>/<hubAccount>/<hubCluster>/<namespace>/<serviceAccount>`, which must
match the `sts:ExternalId` condition in the spoke role's trust policy (step 2).

### 7.6 Install the hub with the AWS service-account annotation

Set `clusterCore.aws.serviceAccountRoleArn` so the chart creates a dedicated
service account for vela-cluster-core annotated with the hub base role:

```bash
helm upgrade --install vela-core ./charts/vela-core \
  --namespace vela-system --create-namespace \
  --set featureGates.enableSpokeClusterCRD=true \
  --set clusterCore.aws.serviceAccountRoleArn=arn:aws:iam::776719623202:role/oam-hub-base \
  --wait
```

This renders a `ServiceAccount` annotated with
`eks.amazonaws.com/role-arn: arn:aws:iam::776...:role/oam-hub-base`, a
ClusterRoleBinding granting it the manager permissions, and points the
vela-cluster-core Deployment at it. (The `eks.amazonaws.com/role-arn` annotation
is used by IRSA; for Pod Identity the association in step 4 is what binds the
identity, and the annotation is harmless and documents intent. If you use IRSA
instead of Pod Identity, this annotation is the binding.)

### 7.7 Connect an EKS spoke

```yaml
apiVersion: core.oam.dev/v1beta1
kind: SpokeCluster
metadata:
  name: prod-us-east-1
  namespace: vela-system
  labels:
    environment: production
    region: us-east-1
    provider: aws
spec:
  mode: connect
  credential:
    type: aws
    aws:
      authMode: podIdentity     # or irsa
      clusterName: prod-us-east-1
      region: us-east-1
      roleArn: arn:aws:iam::627188849628:role/oam-spoke-prod-scoped
      externalId: us-east-1/776719623202/<hub-cluster>/vela-system/vela-core-cluster-core
```

Apply it and watch it connect exactly like the k3d spoke:

```bash
kubectl apply -f spokecluster-prod.yaml
kubectl get spokeclusters -n vela-system -o wide
# NAME             MODE      VERSION    NODES   PLATFORM   STATUS
# prod-us-east-1   connect   v1.30.x    <n>     eks        Connected
```

`platform` shows `eks` (inferred from the `eks.amazonaws.com/*` node labels),
`region` comes from the aws arm, and the controller refreshes the EKS token every
~14 minutes automatically.

### 7.8 IRSA instead of Pod Identity

IRSA is a defined `authMode` and works with the same SpokeCluster shape; only the
hub-side binding differs. Instead of a Pod Identity association you create an
OIDC-backed role whose trust policy federates the hub cluster's OIDC provider and
the vela-cluster-core service account, and you annotate the SA with
`eks.amazonaws.com/role-arn` (which `clusterCore.aws.serviceAccountRoleArn` does).
Flip `authMode: irsa` on the SpokeCluster. Pod Identity is recommended because it
avoids per-cluster OIDC trust wiring and supports cross-account chaining more
directly.

### 7.9 The multi-cluster (fleet) picture

At fleet scale the model is a set of per-cluster roles, one hub base role, and a
`sts:AssumeRole` allow-list:

- One per-cluster scoped role per spoke (in the spoke's own account), each
  permitting `eks:DescribeCluster` on its own ARN and each with an access entry
  on its own cluster.
- One hub base role, associated to the vela-cluster-core SA via Pod Identity,
  whose assume-role permission lists exactly the per-cluster role ARNs it manages
  (never `*`).
- One SpokeCluster per spoke, each naming its per-cluster role.

This keeps every credential single-cluster and makes the hub's blast radius the
explicit allow-list, not the whole fleet. It also composes with the tree
topology: a spoke that is itself a hub for downstream clusters runs its own
vela-cluster-core with its own base role and its own per-cluster roles for its
children; the chains are independent, one hop each.

---

## 8. CLI reference

`kubectl` works directly on the CRD:

```bash
kubectl get spokeclusters                 # default columns
kubectl get spokeclusters -o wide         # + region, endpoint, cpu, memory, latency, auth, last probe
kubectl describe spokecluster <name>
```

The `vela` CLI adds a SpokeCluster-aware group (the legacy `vela cluster list`,
which reads gateway secrets, is unchanged):

```bash
vela cluster spokes list              # aliases: spoke, spokecluster, spokeclusters
vela cluster spokes list -n <ns>      # limit to a namespace (default: all)
vela cluster spokes show <name>       # full detail: spec summary, clusterInfo, conditions
vela cluster spokes show <name> -n <ns>
```

Example:

```
$ vela cluster spokes show prod-us-east-1
Name:        prod-us-east-1
Namespace:   vela-system
Mode:        connect
Auth:        aws
Connection:  Connected

Cluster Info:
  Kubernetes Version: v1.30.4-eks-...
  Platform:           eks
  Region:             us-east-1
  Nodes:              6
  ...

Conditions:
TYPE             STATUS   REASON              MESSAGE
CredentialValid  True     Materialized        credential resolved
Registered       True     SecretMaterialized  cluster-gateway secret written
Connected        True     ProbeSucceeded      spoke API server reachable
InfoSynced       True     DiscoveryOK         cluster info synced
```

---

## 9. Testing

### Unit tests (no cluster, fast)

```bash
unset KUBECONFIG   # avoid the silent os.Exit(1) trap when a stale KUBECONFIG points at a dead path
go test ./pkg/spokecluster/... \
        ./pkg/controller/core.oam.dev/v1beta1/spokecluster/... \
        ./pkg/webhook/core.oam.dev/v1beta1/spokecluster/... -count=1
```

Covers: credential providers (kubeconfig parsing, AWS token format and refresh
math, AWS Materialize with fake STS/EKS clients), the reconcile flow with a fake
client and a mock provider, detach vs orphan deletion, and the webhook
accept/reject/default rules.

### Admission tests (envtest, real apiserver)

Validates CRD-level schema admission (enum rejection, required fields, applied
defaults). Skips automatically when `KUBEBUILDER_ASSETS` is unset:

```bash
export KUBEBUILDER_ASSETS="$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@latest use 1.31.0 -p path)"
go test ./apis/core.oam.dev/v1beta1/ -run TestSpokeClusterAdmission -count=1 -v
```

### End-to-end (multicluster suite)

`test/e2e-multicluster-test/spokecluster_test.go` connects a SpokeCluster to the
worker cluster and asserts Connected + discovery + gateway-secret ownerRef +
detach-on-delete. It self-skips when the SpokeCluster CRD is absent, so it only
runs where vela-cluster-core is deployed with the feature on. The manual k3d
walkthrough in [section 6](#6-reproduce-on-k3d-full-walkthrough) exercises the
same path by hand.

---

## 10. Troubleshooting

| Symptom | Cause and fix |
| --- | --- |
| `kubectl get spokeclusters` errors with "no matches for kind" | The CRD is not installed. Install the chart with `--set featureGates.enableSpokeClusterCRD=true`. |
| SpokeCluster stuck with `CredentialValid=False` | The source Secret is missing or malformed (kubeconfig arm), or AssumeRole/DescribeCluster failed (aws arm). Check the vela-cluster-core pod logs. |
| `Registered=True` but `Connected=False` | The gateway Secret exists but the probe cannot reach the spoke. The endpoint in the credential is not routable from the hub gateway pod (common on k3d if you used `0.0.0.0` or the serverlb IP instead of the `server-0` IP), or a firewall/security group blocks it. |
| `unable to parse bytes as PEM block` from cluster-gateway | k8s 1.35 incompatibility. Pin k3s to `v1.31.5-k3s1`. |
| `aws: The config profile () could not be found` | The devcontainer sets `AWS_PROFILE=""`. Run `unset AWS_PROFILE` before kubectl/helm/aws. |
| `go test` prints a few dots then FAIL with no detail | A stale `KUBECONFIG` points at a nonexistent path and `config.GetConfigOrDie()` exits silently. Run `unset KUBECONFIG`. |
| EKS token works then fails after ~15 min | The refresh loop is not running (the controller is not reconciling this SpokeCluster). Check the pod is up and the requeue is firing; the token is reminted ~1 minute before expiry. |
| AWS spoke `CredentialValid=True` but `Connected=False` with a 401/403 | The assumed role has no Kubernetes access entry on the spoke, or the access policy is missing. Create the access entry and associate `AmazonEKSViewPolicy` (section 7.5, step 3). |
| Cross-account AssumeRole denied | The spoke role's trust `sts:ExternalId` does not match the EKS-generated externalId `<region>/<hubAccount>/<hubCluster>/<namespace>/<serviceAccount>`. Align the SpokeCluster `externalId` and the trust policy. |

To read controller logs:

```bash
kubectl logs -n vela-system deploy/vela-core-cluster-core -f
```

---

## 11. Design decisions and FAQ

**Why a separate `vela-cluster-core` pod instead of adding the controller to
vela-core?** Blast-radius isolation (a cluster-infra bug cannot break application
delivery), independent scaling and lifecycle, and alignment with the KEP's
`vela-cluster-core` engine. Both pods ship in the one chart and share the RBAC.

**Why reuse the cluster-gateway Secret instead of a new connectivity mechanism?**
Everything downstream (read-through, `vela cluster list`, topology dispatch)
already understands that Secret. Materializing it means a SpokeCluster spoke is
indistinguishable from a hand-joined one, and the existing multicluster code path
is reused rather than duplicated.

**Why is the kind `SpokeCluster` and not `Cluster`?** `Cluster` is reserved for
the spoke-side self-reconciling object in later phases, and `spokeclusters`
avoids colliding with the Cluster API `clusters.cluster.x-k8s.io`.

**Why is the credential a discriminated union rather than a flat set of fields?**
It makes invalid states unrepresentable: you cannot set AWS fields on a
kubeconfig spoke, and the auth mode is scoped to its provider so an Azure-style
mode cannot attach to AWS. The webhook enforces exactly one arm matching `type`.

**Why does the AWS SDK sit behind interfaces?** So unit tests never call AWS. The
STS presign and EKS describe calls go through `stsPresignAPI` and `eksDescribeAPI`
seams that tests fake, and the token format and refresh math are tested
deterministically.

**Is the Go admission webhook required?** No. It is opt-in
(`clusterCore.webhook.enabled`, default false) because it needs cert-manager,
which k3d does not have by default. The CRD-level kubebuilder validation markers
(enums, required, min/max) are enforced by the apiserver regardless and are the
admission backstop; the envtest suite confirms them. Turn the Go webhook on where
cert-manager is available for the richer cross-field checks.

**Does hub downtime affect the spoke?** No. The hub only reads; the spoke runs
nothing for this feature and reports nothing. Hub downtime pauses status refresh,
nothing more.

---

## 12. What is next (Phase 2+)

Phase 1 is connect-only. The `SpokeCluster` spec already carries stub fields
(`blueprintRef`, `rolloutStrategyRef`) that the webhook rejects today. Later
phases, per the KEP, add:

- `provision` and `adopt` modes (create or take over a cluster),
- a spoke-side self-reconciling `Cluster` built by vela-cluster-core from a
  dispatched `ClusterBlueprint`,
- `ClusterPlane` / `ClusterBlueprint` composition and versioned rollouts,
- drift detection and cross-cluster inputs.

The vela-cluster-core manager is the home for all of those controllers; this
prototype establishes it with its first resource.

---

## Appendix: files added or changed

Application/library code:

- `apis/core.oam.dev/v1beta1/spokecluster_types.go`, `register.go`, `zz_generated.deepcopy.go`
- `pkg/features/controller_features.go`
- `pkg/spokecluster/credential/{provider,kubeconfig,aws,aws_token}.go` (+ tests)
- `pkg/controller/core.oam.dev/v1beta1/spokecluster/{spokecluster_controller,connect,probe,discovery,deletion}.go` (+ tests)
- `pkg/controller/core.oam.dev/v1beta1/setup.go`
- `pkg/webhook/core.oam.dev/v1beta1/spokecluster/{validation,validating_handler,mutating_handler}.go` (+ tests)
- `cmd/cluster-core/{main.go,app/server.go}`
- `references/cli/spokecluster.go`, `references/cli/cluster.go`

Packaging and tests:

- `charts/vela-core/crds/core.oam.dev_spokeclusters.yaml`
- `charts/vela-core/templates/cluster-core/vela-cluster-core.yaml`
- `charts/vela-core/values.yaml`
- `Dockerfile`, `Makefile`, `.vscode/scripts/k3d-setup.sh`
- `apis/core.oam.dev/v1beta1/spokecluster_admission_test.go`
- `test/e2e-multicluster-test/spokecluster_test.go`
- `go.mod`, `go.sum` (aws-sdk-go-v2)
