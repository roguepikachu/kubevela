#!/usr/bin/env bash
# Give Flux's source-controller on the spoke an AWS identity for ECR pulls through EKS Pod Identity.
# Why: managed node groups run IMDSv2 with hop limit 1, so pods cannot reach the instance metadata
# service and HelmRepository provider "aws" fails with "no EC2 IMDS role found" (seen 2026-10-11).
# Pod Identity needs no network hop: the eks-pod-identity-agent addon (installed by the plane) serves
# credentials to pods whose service account has an association. Idempotent. AWS writes: one IAM role
# and one Pod Identity association on the spoke cluster. Then the source-controller pod is restarted
# through the gateway proxy so it picks up the injected AWS_CONTAINER_* environment.
set -Eeuo pipefail
source "$(dirname "$0")/env.sh"
export KUBECONFIG=$HUB_KC
kubectl() { command kubectl --request-timeout=15s "$@"; }

FLUX_ROLE=${PREFIX}-flux-ecr
FLUX_NS=flux-system
FLUX_SA=source-controller
PROXY=/apis/cluster.core.oam.dev/v1alpha1/clustergateways/$SPOKE/proxy

if ! aws iam get-role --role-name $FLUX_ROLE >/dev/null 2>&1; then
  cat > $OUT/flux-trust.json <<EOF
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"pods.eks.amazonaws.com"},"Action":["sts:AssumeRole","sts:TagSession"],
 "Condition":{"StringEquals":{"aws:RequestTag/eks-cluster-name":"${EKS_NAME}","aws:RequestTag/kubernetes-namespace":"${FLUX_NS}","aws:RequestTag/kubernetes-service-account":"${FLUX_SA}"}}}]}
EOF
  aws iam create-role --role-name $FLUX_ROLE --assume-role-policy-document file://$OUT/flux-trust.json --tags $TAGS \
    --description "Flux source-controller on ${EKS_NAME}: read-only ECR pulls for OCI charts" --query Role.Arn --output text
  aws iam attach-role-policy --role-name $FLUX_ROLE --policy-arn arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly
  echo "REGISTER: IAM role $FLUX_ROLE (AmazonEC2ContainerRegistryReadOnly, trusted by pods.eks.amazonaws.com for $FLUX_NS/$FLUX_SA on $EKS_NAME)"
else
  echo "role $FLUX_ROLE exists"
fi

a=$(aws eks list-pod-identity-associations --cluster-name $EKS_NAME --region $REGION --namespace $FLUX_NS --service-account $FLUX_SA --query 'associations[0].associationId' --output text)
if [ -z "$a" ] || [ "$a" = None ]; then
  a=$(aws eks create-pod-identity-association --cluster-name $EKS_NAME --region $REGION --namespace $FLUX_NS --service-account $FLUX_SA \
    --role-arn arn:aws:iam::${ACCT}:role/$FLUX_ROLE --tags $TAG_MAP --query 'association.associationId' --output text)
  echo "REGISTER: Pod Identity association $a ($FLUX_NS/$FLUX_SA -> $FLUX_ROLE) on $EKS_NAME"
  echo "waiting 30 s for the association to propagate"; sleep 30
else
  echo "association $a exists"
fi

# Restart source-controller so the agent injects the credential environment. Only pods created after
# the association see it.
for p in $(kubectl get --raw "$PROXY/api/v1/namespaces/$FLUX_NS/pods?labelSelector=app%3Dsource-controller" | jq -r '.items[].metadata.name'); do
  kubectl delete --raw "$PROXY/api/v1/namespaces/$FLUX_NS/pods/$p" >/dev/null && echo "restarted $p"
done
for i in $(seq 1 12); do
  sleep 10
  P=$(kubectl get --raw "$PROXY/api/v1/namespaces/$FLUX_NS/pods?labelSelector=app%3Dsource-controller" | jq -r '[.items[] | select(.status.phase=="Running")] | sort_by(.metadata.creationTimestamp) | last | .metadata.name // empty')
  [ -n "$P" ] || continue
  if kubectl get --raw "$PROXY/api/v1/namespaces/$FLUX_NS/pods/$P" | jq -e '.spec.containers[0].env[]? | select(.name=="AWS_CONTAINER_CREDENTIALS_FULL_URI")' >/dev/null; then
    echo "Pod Identity env present on $P"; exit 0
  fi
done
echo "source-controller pod has no Pod Identity env after 2 min; check the agent DaemonSet on the spoke"; exit 1
