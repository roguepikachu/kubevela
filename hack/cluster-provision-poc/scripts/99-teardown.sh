#!/usr/bin/env bash
# Remove everything the POC created, in dependency order. Refuses to run without --yes.
# Keeps: the EKS cluster chub1, the Connect install, and the Warn jsPolicies.
set -uo pipefail
[ "${1:-}" = --yes ] || { echo "refusing: pass --yes (this deletes an EKS cluster and IAM roles)"; exit 1; }
source "$(dirname "$0")/env.sh"
export KUBECONFIG=$HUB_KC
CLUSTERCTL=$BIN/clusterctl

# 1. SpokeCluster (retain keeps the CAPI objects), then delete the CAPI Cluster explicitly so CAPA removes EKS
kubectl delete spokeclusters.core.oam.dev $SPOKE -n $NS --ignore-not-found --timeout=2m
kubectl delete applications.core.oam.dev sc-$SPOKE-infra -n $NS --ignore-not-found --timeout=2m
kubectl delete applications.core.oam.dev -n $NS -l spokecluster.core.oam.dev/name=$SPOKE,spokecluster.core.oam.dev/role=bootstrap --ignore-not-found --timeout=2m
kubectl delete applications.core.oam.dev -n $NS -l spokecluster.core.oam.dev/name=$SPOKE,spokecluster.core.oam.dev/role=dispatch --ignore-not-found --timeout=2m   # slice 2b; gc never, so the planes stay on the spoke until EKS goes
kubectl delete clusters.cluster.x-k8s.io $SPOKE -n $NS --ignore-not-found --timeout=20m
for i in $(seq 1 30); do
  s=$(aws eks describe-cluster --region $REGION --name $EKS_NAME --query 'cluster.status' --output text 2>/dev/null || echo absent)
  echo "$(date -u +%H:%M) EKS $EKS_NAME: $s"; [ "$s" = absent ] && break; sleep 60
done
[ "$s" = absent ] || { echo "EKS $EKS_NAME is still $s; fix CAPA and re-run before tearing down CAPI"; exit 1; }
kubectl delete clusterblueprints.core.oam.dev/eks-capi clusterplanes.core.oam.dev/eks-capi-foundation componentdefinitions.core.oam.dev/eks-capi -n $NS --ignore-not-found
# Bootstrap plane and its definition (slice 2a). The spoke's Flux and vela runtime that this plane
# installed is never garbage-collected by the hub; it goes away only with the EKS cluster above.
kubectl delete clusterplanes.core.oam.dev/foundation-runtime componentdefinitions.core.oam.dev/helm-flux -n $NS --ignore-not-found
# clusterInit planes and blueprint (slice 2b). Their ClusterBlueprintRevisions and ClusterPlaneRevisions are owned by these and go with them.
kubectl delete clusterblueprints.core.oam.dev/cpspoke1-platform clusterplanes.core.oam.dev/foundation-policy clusterplanes.core.oam.dev/foundation-certs -n $NS --ignore-not-found

# 2. CAPI
$CLUSTERCTL delete --kubeconfig $HUB_KC --all --include-crd --include-namespace || true
kubectl delete ns cert-manager-test --ignore-not-found
a=$(aws eks list-pod-identity-associations --cluster-name $HUB --region $REGION --namespace $CAPA_NS --service-account $CAPA_SA --query 'associations[0].associationId' --output text)
[ -n "$a" ] && [ "$a" != None ] && aws eks delete-pod-identity-association --cluster-name $HUB --region $REGION --association-id $a

# 3. IAM
# Operator access entry from 16-spoke-access.sh. Same role-ARN reconstruction as there: the STS
# assumed-role ARN drops the IAM path, so the name is resolved through iam get-role. Step 1 normally
# removed the EKS cluster already, and the entry went with it, so a gone cluster is tolerated.
op_name=$(aws sts get-caller-identity --query Arn --output text 2>/dev/null | sed -E 's#.*:assumed-role/([^/]+)/.*#\1#')
op_arn=$(aws iam get-role --role-name "$op_name" --query Role.Arn --output text 2>/dev/null || echo "")
if [ -n "$op_arn" ] && aws eks describe-access-entry --cluster-name $EKS_NAME --region $REGION --principal-arn "$op_arn" >/dev/null 2>&1; then
  aws eks delete-access-entry --cluster-name $EKS_NAME --region $REGION --principal-arn "$op_arn" && echo "deleted EKS access entry $op_arn on $EKS_NAME"
else
  echo "no operator access entry to delete on $EKS_NAME (cluster gone or entry never created)"
fi
rm -f /tmp/cp-spoke.kubeconfig
# Flux ECR identity from 12-spoke-flux-identity.sh. The association lives on the spoke EKS cluster,
# which step 1 normally removed already (its associations go with it), so a failed list is tolerated.
fa=$(aws eks list-pod-identity-associations --cluster-name $EKS_NAME --region $REGION --namespace flux-system --service-account source-controller --query 'associations[0].associationId' --output text 2>/dev/null || echo None)
[ -n "$fa" ] && [ "$fa" != None ] && aws eks delete-pod-identity-association --cluster-name $EKS_NAME --region $REGION --association-id $fa && echo "deleted Pod Identity association $fa (flux-system/source-controller)"
aws iam detach-role-policy --role-name ${PREFIX}-flux-ecr --policy-arn arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly 2>/dev/null
aws iam delete-role --role-name ${PREFIX}-flux-ecr 2>/dev/null && echo "deleted role ${PREFIX}-flux-ecr"
aws iam delete-role-policy --role-name $HUB_ROLE --policy-name assume-cp-spoke-roles || true
aws iam delete-role-policy --role-name $SPOKE_ROLE --policy-name describe-own-cluster || true
for i in 1 2 3 4 5 6; do arn=arn:aws:iam::${ACCT}:policy/${CAPA_ROLE}-$i; aws iam detach-role-policy --role-name $CAPA_ROLE --policy-arn $arn 2>/dev/null; aws iam delete-policy --policy-arn $arn 2>/dev/null; done
aws iam detach-role-policy --role-name $CP_ROLE --policy-arn arn:aws:iam::aws:policy/AmazonEKSClusterPolicy 2>/dev/null
for p in AmazonEKSWorkerNodePolicy AmazonEKS_CNI_Policy AmazonEC2ContainerRegistryReadOnly; do aws iam detach-role-policy --role-name $NODE_ROLE --policy-arn arn:aws:iam::aws:policy/$p 2>/dev/null; done
for r in $CAPA_ROLE $CP_ROLE $NODE_ROLE $SPOKE_ROLE; do aws iam delete-role --role-name $r 2>/dev/null && echo "deleted role $r"; done

# 4. ECR (the hub still runs this image: roll vela-core back to the Connect image first if it must keep running)
aws ecr delete-repository --repository-name $REPO --region $REGION --force 2>/dev/null && echo "deleted ECR repo $REPO"
aws ecr delete-repository --repository-name $PREFIX/vela-core --region $REGION --force 2>/dev/null && echo "deleted ECR repo $PREFIX/vela-core"   # the OCI chart pushed by 08-chart.sh

echo "== still tagged purpose=cluster-provision-poc (expect nothing):"
aws resourcegroupstaggingapi get-resources --region $REGION --tag-filters Key=purpose,Values=cluster-provision-poc --query 'ResourceTagMappingList[].ResourceARN' --output text
