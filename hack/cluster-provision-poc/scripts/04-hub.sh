#!/usr/bin/env bash
# Upgrade vela-core on the hub to the POC image. Helm does not upgrade CRDs from crds/, so the
# new ClusterPlane/ClusterBlueprint CRDs and the changed SpokeCluster CRD are applied first.
set -euo pipefail
source "$(dirname "$0")/env.sh"
TAG=$(git -C $WORKTREE rev-parse --short HEAD)

# Pod Identity association for vela-system/vela-core-cluster-core -> the Connect hub role. The Connect
# demo's 04-hub.sh used to own this; a recreated scratch hub has none, so the POC creates it when missing.
# Pods created within seconds of a new association get no AWS credentials, hence the 30 s wait.
a=$(aws eks list-pod-identity-associations --cluster-name $HUB --region $REGION --namespace $NS --service-account $SA --query 'associations[0].associationId' --output text)
if [ -z "$a" ] || [ "$a" = None ]; then
  aws eks create-pod-identity-association --cluster-name $HUB --region $REGION --namespace $NS --service-account $SA \
    --role-arn arn:aws:iam::${ACCT}:role/$HUB_ROLE --tags $TAG_MAP --query 'association.associationId' --output text
  echo "REGISTER: Pod Identity association $NS/$SA -> $HUB_ROLE on $HUB"; echo "waiting 30 s for the association to propagate"; sleep 30
else
  echo "hub association $a exists"
fi

kubectl --kubeconfig $HUB_KC apply --server-side --force-conflicts -f $WORKTREE/charts/vela-core/crds/
cd $WORKTREE
HELM_LOG="$OUT/helm-upgrade.log"
if ! helm --kubeconfig $HUB_KC upgrade --install vela-core charts/vela-core --namespace $NS --create-namespace \
  --set featureGates.enableClusterInfrastructure=true \
  --set image.repository=$ECR/$REPO --set image.tag=$TAG --set image.pullPolicy=IfNotPresent \
  --force-conflicts --wait --wait-for-jobs --timeout 12m >"$HELM_LOG" 2>&1; then
  echo "helm upgrade FAILED, last lines of $HELM_LOG:"; grep -v 'warnings.go' "$HELM_LOG" | tail -5; exit 1
fi
grep -E '^(Release|STATUS)' "$HELM_LOG" || true
kubectl --kubeconfig $HUB_KC rollout status deploy/vela-core-cluster-core -n $NS --timeout=5m
P=$(kubectl --kubeconfig $HUB_KC get pod -n $NS -l controller.oam.dev/name=vela-cluster-core --field-selector=status.phase=Running --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1].metadata.name}')
kubectl --kubeconfig $HUB_KC get pod $P -n $NS -o jsonpath='{.spec.containers[0].env[*].name}' | grep -q AWS_CONTAINER_CREDENTIALS_FULL_URI && echo "Pod Identity env present on $P" || { echo "MISSING Pod Identity env on $P: kubectl rollout restart deploy/vela-core-cluster-core -n $NS"; exit 1; }
kubectl --kubeconfig $HUB_KC get crd clusterblueprints.core.oam.dev clusterplanes.core.oam.dev -o name
echo "REGISTER (modification): helm release vela-core on chub1 now at $ECR/$REPO:$TAG (was tenant-gokarna-cc-runbook:b07614660)"
