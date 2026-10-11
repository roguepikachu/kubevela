# SpokeCluster provision, adopt and bootstrap examples

Written 2026-10-11 against `poc/cluster-provision`. Everything here was applied to a real hub
(EKS) and produced a real EKS spoke through Cluster API Provider AWS, then installed Flux and
vela-core on that spoke through the cluster-gateway. Account ids, VPC ids and role names are
placeholders; the shape is exact.

Read `../spokecluster-connect/README.md` first for the credential and gateway Secret model. This
directory adds what sits in front of it: how the cluster comes to exist, and how its runtime gets
installed.

## The order things happen in

1. You apply a `ClusterPlane` whose single component is a substrate definition (`01`), and a
   `ClusterBlueprint` that references it (`02`).
2. You apply a `SpokeCluster` with `mode: provision` (`03`). The controller renders the blueprint
   into one hub `Application` named `sc-<spoke>-infra-<uid8>` (plus `-<n>` after each release) with
   the policies `apply-once` and `garbage-collect` strategy `never`. vela-core applies the
   substrate's CRs on the hub; the substrate controller (CAPA here) builds the cluster.
3. When every substrate component is healthy the `InfraProvisioned` condition turns True and the
   controller hands over to the connect path: it assumes the spoke role, materializes the gateway
   Secret, probes and discovers. `Connected` turns True. Nothing after this point knows which
   substrate was used.
4. If `spec.bootstrap.planeRef` is set, every Connected pass renders that plane into a second
   Application `sc-<spoke>-bootstrap` targeted at the spoke through the gateway (`05`, `06`). The
   `Bootstrapped` condition mirrors its health.

Measured on EKS: control plane ready in about 10 minutes, Connected at 11, Flux plus vela-core on
the spoke about 10 minutes later, a chart version change on the plane reaching the spoke in under
a minute.

## Modes in one paragraph

`connect` never touches infrastructure. `provision` creates it and refuses objects that already
exist but are managed by no Application (`InfraProvisioned=False`, reason
`InfraUnmanagedExists`). `adopt` claims such objects with the `take-over` policy (`04`). Allowed
transitions: connect to adopt, provision to adopt, and provision or adopt back to connect, which
releases the infrastructure (the Application is deleted under its `never` rule, labels are
stripped, the cluster stays). Connect to provision is rejected by the CRD and the webhook: release
first, then adopt. Deleting a SpokeCluster with the default `infraDeletionPolicy: retain` keeps the
cluster; `delete` removes it with the SpokeCluster.

## Files

| File | What it shows |
|---|---|
| `01-clusterplane-eks-capi-foundation.yaml` | The substrate plane: one `eks-capi` component carrying the shared parameter contract |
| `02-clusterblueprint-eks-capi.yaml` | The blueprint that `infraProvisioning.blueprintRef` points at |
| `03-spokecluster-provision.yaml` | Create an EKS cluster and connect to it |
| `04-spokecluster-adopt.yaml` | Claim an existing or retained cluster's substrate objects |
| `05-clusterplane-foundation-runtime.yaml` | The bootstrap plane: namespaces, Flux, vela-core as a Flux HelmRelease from an OCI chart |
| `06-spokecluster-connect-with-bootstrap.yaml` | Bootstrap an existing cluster you only connect to |
| `07-invalid-examples.yaml` | Shapes admission rejects, with the message you will see |

## Prerequisites the examples assume

- The hub runs vela-core with `featureGates.enableClusterInfrastructure=true` and cluster-gateway.
- The substrate controller is installed on the hub with its own cloud identity (for CAPA: Pod
  Identity on `capa-system/capa-controller-manager`), and `EXP_MACHINE_POOL` is on.
- IAM roles exist for the control plane, the nodes and the spoke (the spoke role is trusted by the
  hub's role with the external id EKS derives from the hub cluster, namespace and service account).
- The `eks-capi` and `helm-flux` ComponentDefinitions from `vela-templates/definitions/cluster-infra`
  are applied in `vela-system`.
- For the bootstrap plane: the vela-core chart is pushed to a registry as an OCI artifact and Flux's
  source-controller on the spoke can authenticate to it (on EKS managed node groups pods cannot
  reach IMDS, so give `flux-system/source-controller` an EKS Pod Identity association to a read-only
  registry role).

## Things that bit us, so you do not have to find them again

- Put namespaces in their own plane component. vela-core dry-runs every object of a component
  before creating any of them, so a Namespace in the same component as its tenants fails the dry
  run with `namespaces "x" not found`.
- Keep `multicluster.enabled: true` for vela-core on the spoke. `vela-cluster-core` waits for the
  cluster-gateway APIService at startup and never becomes ready without it.
- Same-VPC hub and spoke: the spoke's control plane security group needs an ingress rule from the
  VPC CIDR or the hub's probe times out (`controlPlaneIngressCidrs` in the plane).
- Upgrades of resources under a `garbage-collect` `never` rule need the stale-revision fix on this
  branch; without it a plane edit never reaches the spoke while the old release is healthy.
