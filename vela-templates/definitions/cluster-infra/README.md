# Cluster infrastructure definitions

Substrate ComponentDefinitions for `SpokeCluster` modes `provision` and `adopt`. Each
definition turns one provisioning tool's objects into a component that a `ClusterPlane`
can carry and the hub renders through an ordinary Application. They are not shipped in the
vela-core chart (the generator scans only `internal`, `registry` and `deprecated`); apply
them to a hub with `vela def apply <file> -n vela-system`.

## The contract every substrate definition keeps

All EKS substrate definitions accept the same parameters, so a blueprint can switch tools
by changing one component `type` and nothing else:

| Parameter | Meaning |
| --- | --- |
| `name` | object name prefix on the hub; also the CAPI or substrate object name |
| `eksClusterName` | EKS cluster name in AWS (defaults to `name`) |
| `region` | AWS region |
| `version` | Kubernetes version in the form the substrate expects (`v1.35` for CAPA, `1.35` for ACK and Crossplane; the definition converts) |
| `vpcId`, `subnetIds` | bring-your-own network; the definition must not create a VPC |
| `controlPlaneRoleName`, `nodeRoleName` | pre-created IAM roles; the definition never creates IAM |
| `spokePrincipalArn` | IAM principal granted `AmazonEKSClusterAdminPolicy` through an EKS access entry, so the hub can reach the cluster through the SpokeCluster credential |
| `controlPlaneIngressCidrs` | CIDRs allowed to reach the API server on 443 besides the cluster's nodes; a same-VPC hub needs its VPC CIDR here |
| `addons` | EKS addons with explicit versions |
| `nodes` | one managed node group: `instanceType`, `min`, `max`, `desired` |
| `tags` | tags on every AWS resource the substrate creates |
| `adopt` | the plane records that the cluster already exists; the definition renders the substrate's import form where one exists |

Required outputs, the same for every substrate: the cluster reachable under
`eksClusterName` in `region`, `API_AND_CONFIG_MAP` authentication, the access entry for
`spokePrincipalArn`, and a primary `output` whose `status` the health policy can read so the
hub knows when the control plane is ready. Kubeconfig Secrets a tool may write are not part
of the contract.

Health and status: `healthPolicy` must default `isHealth` to `false` when the primary object
has no status yet, and `customStatus.message` must be a CUE default so a failure message can
override it. The CAPI definition shows the pattern.

The parameter contract applies only to infra planes. Components a bootstrap plane carries,
such as `helm-flux`, are ordinary ComponentDefinitions with whatever parameters their tool
needs; they share this directory and its health and status conventions, nothing more.

## Registry

| Definition | Substrate | Primary output | Status |
| --- | --- | --- | --- |
| `component/eks-capi.cue` | Cluster API Provider AWS v2.13.1, CAPI v1.14.3 | `AWSManagedControlPlane` (`status.ready`) | proven on AWS 2026-10-09: provision, retain, re-adopt |
| `component/eks-crossplane.cue` | Crossplane upbound AWS providers | `Cluster.eks.aws.upbound.io` | planned |
| `component/eks-ack.cue` | AWS Controllers for Kubernetes, EKS controller | `Cluster.eks.services.k8s.aws` | planned |
| `component/eks-terraform.cue` | tofu-controller with terraform-aws-modules/eks | `Terraform` | planned |
| `component/helm-flux.cue` | Flux v2 source-controller and helm-controller on the target cluster; installs a chart as a HelmRelease from a HelmRepository (`source.toolkit.fluxcd.io/v1`, `helm.toolkit.fluxcd.io/v2`) | `HelmRelease` (`status.conditions`, type `Ready`) | used by the `foundation-runtime` bootstrap plane |

Adding a substrate: copy the parameter block from `eks-capi.cue` unchanged, render that
tool's objects, keep the health and status pattern, run `vela def vet`, and add a row here.

## Workflow steps

`workflowstep/` holds the steps the hub Application runs to drive one spoke. Apply them like the
components, with `vela def apply <file> -n vela-system` (`scripts/22-oam-defs.sh` in the POC does
this through server-side apply). Both use the `vela/kube` and `vela/builtin` providers: `kube.#Read`
and `kube.#Apply` take the target spoke as `$params.cluster`, the cluster-gateway name, and act on
the hub itself when it is left empty.

`wait-spoke-connected` reads the SpokeCluster named in its parameters (the namespace defaults to the
Application's) and holds the workflow with a conditional wait until a condition of type `Connected`
has status `True`, that is until the spoke is reachable through the cluster gateway. A SpokeCluster
with no conditions yet simply keeps the step waiting.

`dispatch-blueprint` ships one blueprint revision. On the hub it reads the ClusterBlueprintRevision,
every ClusterPlaneRevision it pins and the ComponentDefinitions named in `definitions`, trims each to
apiVersion, kind, name, namespace, labels and spec, and applies them into the same namespace on the
spoke. Then it applies a Cluster object there (named `clusterName`, defaulting to `cluster`) whose
three lifecycle stages all reference that revision, reads the Cluster back from the spoke and waits
until `status.phase` is set and no longer `Pending`. That is the spoke's own vela-cluster-core
acknowledging the revision; whether the planes come up healthy is reported by the hub's status
pull, not by this step.

### One Application per SpokeCluster

A SpokeCluster owns a single Application on the hub. Its components come from the planes the
SpokeCluster references: the substrate components of the infra planes (an `eks-capi` cluster, for
example) and the components of the bootstrap plane (`namespaces`, `flux`, `vela-core`). Its policies
are `hub` and `spoke`, which place components on the hub's local cluster or on the spoke through the
gateway, `infra` and `runtime`, which select which components each step deploys, `keep`, which
leaves everything in place when the Application goes, and `adopt`, which takes over objects that
already exist. Its workflow runs four steps in order: `provision` deploys the infra components on
the hub, `connected` is `wait-spoke-connected`, `bootstrap` deploys the runtime components onto the
spoke, and `platform` is `dispatch-blueprint`.
