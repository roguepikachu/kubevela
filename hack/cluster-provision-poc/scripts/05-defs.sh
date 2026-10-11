#!/usr/bin/env bash
# Apply the eks-capi ComponentDefinition, then a ClusterPlane whose single component is the
# EKS cluster, and the ClusterBlueprint that references it. Addon versions are resolved from
# EKS for the target Kubernetes version so the plane never carries stale pins.
set -euo pipefail
source "$(dirname "$0")/env.sh"
export KUBECONFIG=$HUB_KC
/tmp/vela def apply $POC/defs/eks-capi.cue -n $NS

addon_version() { aws eks describe-addon-versions --region $REGION --kubernetes-version $K8S_VERSION --addon-name $1 --query 'addons[0].addonVersions[0].addonVersion' --output text; }
V_CNI=$(addon_version vpc-cni); V_PROXY=$(addon_version kube-proxy); V_DNS=$(addon_version coredns); V_PIA=$(addon_version eks-pod-identity-agent)
case "$V_CNI$V_PROXY$V_DNS$V_PIA" in *None*) echo "an addon version resolved to None for Kubernetes $K8S_VERSION: $V_CNI $V_PROXY $V_DNS $V_PIA"; exit 1;; esac
SUBNET_JSON=$(printf '%s\n' $SUBNETS | jq -R . | jq -sc .)
VPC_CIDR=$(aws ec2 describe-vpcs --region $REGION --vpc-ids $VPC --query 'Vpcs[0].CidrBlock' --output text)

cat > $OUT/plane-eks-capi.yaml <<EOF
apiVersion: core.oam.dev/v1beta1
kind: ClusterPlane
metadata:
  name: eks-capi-foundation
  namespace: ${NS}
spec:
  description: EKS cluster in the hub VPC, created by Cluster API Provider AWS
  components:
    - name: cluster
      type: eks-capi
      properties:
        name: ${SPOKE}
        eksClusterName: ${EKS_NAME}
        region: ${REGION}
        version: v${K8S_VERSION}
        vpcId: ${VPC}
        subnetIds: ${SUBNET_JSON}
        controlPlaneRoleName: ${CP_ROLE}
        nodeRoleName: ${NODE_ROLE}
        spokePrincipalArn: arn:aws:iam::${ACCT}:role/${SPOKE_ROLE}
        controlPlaneIngressCidrs: ["${VPC_CIDR}"]
        addons:
          - {name: vpc-cni, version: "${V_CNI}"}
          - {name: kube-proxy, version: "${V_PROXY}"}
          - {name: coredns, version: "${V_DNS}"}
          - {name: eks-pod-identity-agent, version: "${V_PIA}"}
        nodes: {instanceType: m5.large, min: 1, max: 3, desired: 2}
        tags: {purpose: cluster-provision-poc, owner: aykumar}
---
apiVersion: core.oam.dev/v1beta1
kind: ClusterBlueprint
metadata:
  name: eks-capi
  namespace: ${NS}
spec:
  description: One-plane blueprint, the CAPI substrate
  planes:
    - name: foundation
      ref: {name: eks-capi-foundation}
EOF
kubectl apply -f $OUT/plane-eks-capi.yaml
kubectl get componentdefinition eks-capi -n $NS -o jsonpath='{.metadata.name}{"\n"}'
kubectl get clusterplanes.core.oam.dev,clusterblueprints.core.oam.dev -n $NS
