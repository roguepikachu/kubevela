# SpokeCluster examples

Generated 2026-07-31 against `feat/cluster-kep-infrastructure` @ `6a266aba1`.

## The thing to understand first: there are two Secrets

They are easy to conflate and they have opposite ownership.

| | Credential Secret | Gateway Secret |
|---|---|---|
| Who creates it | **You, by hand** | The controller |
| Where | The SpokeCluster's own namespace (`secretRef.namespace` must match if set) | Always `vela-system` |
| Name | Whatever you choose | Same as the SpokeCluster's name |
| Contents | A kubeconfig under key `kubeconfig` | `endpoint`, `ca.crt`, and either `token` or `tls.crt`/`tls.key` |
| Applies to | `credential.type: kubeconfig` only | Every credential type |
| Example file | `00-credential-secret-kubeconfig.yaml` | `99-gateway-secret-reference.yaml` (reference only, do not apply) |

We manually create the kubeconfig and a Secret for it is **but only for the kubeconfig credential type**, and the Secret you create is the input, not the cluster-gateway registration. The controller reads your Secret, materializes the credential, and writes a second Secret in `vela-system` that cluster-gateway actually consumes. That output Secret is deliberately bit-compatible with what `vela cluster join` writes, so existing consumers cannot tell the two apart.

For `credential.type: aws` you create **no Secret at all**. The hub uses its ambient Pod Identity or IRSA identity, assumes the per-cluster role, calls `eks:DescribeCluster`, and mints a short-lived EKS bearer token (`k8s-aws-v1.`). The real token lifetime is **15 minutes** (STS GetCallerIdentity presign window); the controller schedules remint at **+13 minutes** (`Materialized.NextRefresh`, a 2-minute lead) and `nextRequeue` is `min(probeIntervalSeconds, time until NextRefresh)`. Cached credentials are reused until that deadline. Keep the probe interval under about 13 minutes if you want continuous Connected probes between remints.

## What the kubeconfig must look like

The provider is stricter than `kubectl`. From `pkg/spokecluster/credential/kubeconfig.go`:

- The `current-context` is used. It must exist, and its cluster and user must resolve.
- `certificate-authority-data` must be **inline**. A file-path `certificate-authority` is rejected, because the path refers to the machine that produced the kubeconfig, not the hub controller.
- Auth must be an **embedded** `token`, or an embedded `client-certificate-data` plus `client-key-data`. Exec plugins (`aws eks get-token`, `gke-gcloud-auth-plugin`) and file-path credentials are rejected.
- `tls-server-name`, if set, must equal the endpoint host. cluster-gateway always derives the verified ServerName from the endpoint, so a differing value is rejected at registration rather than producing a spoke that registers and then fails every handshake.
- The cluster `server` must be `https` and must not target hub-internal DNS (`*.svc`, `*.cluster.local`, `kubernetes.default…`) or cloud metadata/link-local addresses (`169.254.0.0/16`, loopback, Azure `168.63.129.16`, AWS IMDS IPv6). RFC1918 endpoints (for example k3d Docker IPs) and public cloud API hostnames (for example `*.eks.amazonaws.com`) are allowed.
- `insecure-skip-tls-verify: true` causes the CA to be dropped, which registers an unverified connection. Avoid.

## Files

| File | What it shows |
|---|---|
| `00-credential-secret-kubeconfig.yaml` | The input Secret you create by hand |
| `01-spokecluster-kubeconfig-minimal.yaml` | Smallest valid SpokeCluster |
| `02-spokecluster-kubeconfig-full.yaml` | Every optional field set explicitly |
| `03-spokecluster-aws-podidentity.yaml` | EKS via Pod Identity, no Secret needed |
| `04-spokecluster-aws-irsa.yaml` | Same, IRSA variant |
| `05-spokecluster-tenant-namespace.yaml` | `detach` outside `vela-system` |
| `06-spokecluster-orphan.yaml` | `deletionPolicy: orphan` |
| `07-spokecluster-phase2-stubs.yaml` | Phase 2 fields that exist but are inert |
| `08-invalid-examples.yaml` | Cases that should be rejected, with the expected error |
| `99-gateway-secret-reference.yaml` | What the controller produces, for reading only |

## Applying them

The feature gate must be on, or nothing reconciles and status stays empty. The SpokeCluster admission webhook is enabled by default whenever the gate is on (`clusterCore.webhook.enabled=true`); set it to `false` only if you intentionally want CRD-schema-only admission:

```
helm install vela-core charts/vela-core -n vela-system --create-namespace \
  --set featureGates.enableClusterInfrastructure=true
```

For production, prefer cert-manager so the validating webhook can use `failurePolicy: Fail` from the first apply (job-patch installs briefly with `Ignore` until the patch Job rewrites the CA and policy):

```
helm install vela-core charts/vela-core -n vela-system --create-namespace \
  --set featureGates.enableClusterInfrastructure=true \
  --set admissionWebhooks.certManager.enabled=true
```

Note the gate does **not** control whether the CRD exists. Helm applies `crds/` unconditionally, so `kubectl get spokeclusters` works either way; with the gate off the objects simply never get a status.
