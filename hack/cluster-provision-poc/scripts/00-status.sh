#!/usr/bin/env bash
# Read-only status of every piece of the provisioning POC. Writes the hub kubeconfig.
set -uo pipefail
source "$(dirname "$0")/env.sh"
ok() { printf '  ok       %s\n' "$*"; }; miss() { printf '  MISSING  %s\n' "$*"; }

echo "== AWS session"
aws sts get-caller-identity --query Arn --output text >/dev/null 2>&1 && ok "$(aws sts get-caller-identity --query Arn --output text)" || { miss "AWS session (refresh ~/.dev/aws-envs/scratch2.env)"; exit 1; }
aws eks update-kubeconfig --region $REGION --name $HUB --kubeconfig $HUB_KC >/dev/null

echo "== Code"
[ -d $WORKTREE ] && ok "worktree $WORKTREE at $(git -C $WORKTREE rev-parse --short HEAD)" || miss "worktree $WORKTREE"
TAG=$(git -C $WORKTREE rev-parse --short HEAD 2>/dev/null || echo none)
aws ecr describe-images --repository-name $REPO --region $REGION --image-ids imageTag=$TAG >/dev/null 2>&1 && ok "$ECR/$REPO:$TAG" || miss "image $ECR/$REPO:$TAG"

echo "== IAM"
for r in $CAPA_ROLE $CP_ROLE $NODE_ROLE $SPOKE_ROLE; do aws iam get-role --role-name $r >/dev/null 2>&1 && ok "role $r" || miss "role $r"; done
aws iam get-role-policy --role-name $HUB_ROLE --policy-name assume-cp-spoke-roles >/dev/null 2>&1 && ok "hub role policy assume-cp-spoke-roles" || miss "hub role policy assume-cp-spoke-roles"

echo "== CAPI on the hub"
kubectl --kubeconfig $HUB_KC get deploy -n $CAPA_NS capa-controller-manager >/dev/null 2>&1 && ok "capa-controller-manager $(kubectl --kubeconfig $HUB_KC get deploy -n $CAPA_NS capa-controller-manager -o jsonpath='{.status.readyReplicas}/{.spec.replicas}')" || miss "capa-controller-manager"
a=$(aws eks list-pod-identity-associations --cluster-name $HUB --region $REGION --namespace $CAPA_NS --service-account $CAPA_SA --query 'associations[0].associationId' --output text 2>/dev/null)
[ -n "$a" ] && [ "$a" != None ] && ok "Pod Identity association $a (capa)" || miss "Pod Identity association for $CAPA_NS/$CAPA_SA"

echo "== KubeVela on the hub"
img=$(kubectl --kubeconfig $HUB_KC get deploy -n $NS vela-core-cluster-core -o jsonpath='{.spec.template.spec.containers[0].image}' 2>/dev/null)
[ "$img" = "$ECR/$REPO:$TAG" ] && ok "cluster-core image $img" || miss "cluster-core image is $img, want $ECR/$REPO:$TAG"
kubectl --kubeconfig $HUB_KC get crd clusterblueprints.core.oam.dev >/dev/null 2>&1 && ok "CRD clusterblueprints" || miss "CRD clusterblueprints"
kubectl --kubeconfig $HUB_KC get componentdefinition eks-capi -n $NS >/dev/null 2>&1 && ok "ComponentDefinition eks-capi" || miss "ComponentDefinition eks-capi"
kubectl --kubeconfig $HUB_KC get clusterblueprints.core.oam.dev eks-capi -n $NS >/dev/null 2>&1 && ok "ClusterBlueprint eks-capi" || miss "ClusterBlueprint eks-capi"

echo "== Spoke"
kubectl --kubeconfig $HUB_KC get spokeclusters.core.oam.dev $SPOKE -n $NS >/dev/null 2>&1 && ok "SpokeCluster $SPOKE $(kubectl --kubeconfig $HUB_KC get spokeclusters.core.oam.dev $SPOKE -n $NS -o jsonpath='{.status.connection} provisioning={.status.provisioning.phase}')" || miss "SpokeCluster $SPOKE (absent)"
aws eks describe-cluster --region $REGION --name $EKS_NAME --query 'cluster.status' --output text 2>/dev/null | grep -q . && ok "EKS $EKS_NAME $(aws eks describe-cluster --region $REGION --name $EKS_NAME --query 'cluster.status' --output text)" || miss "EKS $EKS_NAME (absent)"
echo "== Tagged AWS resources"
aws resourcegroupstaggingapi get-resources --region $REGION --tag-filters Key=purpose,Values=cluster-provision-poc --query 'ResourceTagMappingList[].ResourceARN' --output text | tr '\t' '\n' | sed 's/^/  /'
