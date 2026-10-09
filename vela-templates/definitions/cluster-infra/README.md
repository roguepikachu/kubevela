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

## Registry

| Definition | Substrate | Primary output | Status |
| --- | --- | --- | --- |
| `component/eks-capi.cue` | Cluster API Provider AWS v2.13.1, CAPI v1.14.3 | `AWSManagedControlPlane` (`status.ready`) | proven on AWS 2026-10-09: provision, retain, re-adopt |
| `component/eks-crossplane.cue` | Crossplane upbound AWS providers | `Cluster.eks.aws.upbound.io` | planned |
| `component/eks-ack.cue` | AWS Controllers for Kubernetes, EKS controller | `Cluster.eks.services.k8s.aws` | planned |
| `component/eks-terraform.cue` | tofu-controller with terraform-aws-modules/eks | `Terraform` | planned |

Adding a substrate: copy the parameter block from `eks-capi.cue` unchanged, render that
tool's objects, keep the health and status pattern, run `vela def vet`, and add a row here.
