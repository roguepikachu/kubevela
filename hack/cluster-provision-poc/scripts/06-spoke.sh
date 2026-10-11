#!/usr/bin/env bash
# Apply the provision-mode SpokeCluster and watch it through Provisioning to Connected.
# EKS control plane takes 10-15 min, the node group another 3-5. Prints a line a minute.
set -euo pipefail
source "$(dirname "$0")/env.sh"
export KUBECONFIG=$HUB_KC
kubectl() { command kubectl --request-timeout=15s "$@"; }   # a dead network must fail fast, not hang the wait loops (run 3 step 6 hung for hours)
kubectl apply -f $POC/manifests/spokecluster-$SPOKE.yaml
echo "REGISTER NOW: SpokeCluster $SPOKE (creating), Application sc-$SPOKE-infra, EKS $EKS_NAME (creating)"
for i in $(seq 1 45); do
  sleep 60
  conn=$(kubectl get spokeclusters.core.oam.dev $SPOKE -n $NS -o jsonpath='{.status.connection}' || echo ?)
  prov=$(kubectl get spokeclusters.core.oam.dev $SPOKE -n $NS -o jsonpath='{.status.provisioning.phase} healthy={.status.provisioning.healthy}' || echo ?)
  eks=$(aws eks describe-cluster --region $REGION --name $EKS_NAME --query 'cluster.status' --output text 2>/dev/null || echo absent)
  cp=$(kubectl get awsmanagedcontrolplanes.controlplane.cluster.x-k8s.io $SPOKE-control-plane -n $NS -o jsonpath='{.status.ready}' 2>/dev/null || echo -)
  echo "$(date -u +%H:%M) connection=$conn provisioning=$prov eks=$eks controlplane.ready=$cp"
  [ "$conn" = Connected ] && break
done
kubectl get spokeclusters.core.oam.dev $SPOKE -n $NS -o jsonpath='{range .status.conditions[*]}  {.type}={.status} ({.reason}) {.message}{"\n"}{end}'
kubectl get spokeclusters.core.oam.dev -n $NS -o wide
kubectl get applications.core.oam.dev -n $NS -l spokecluster.core.oam.dev/name=$SPOKE,spokecluster.core.oam.dev/role=infra -o jsonpath='{.items[0].metadata.name} {.items[0].status.status} {range .items[0].status.services[*]}{.name}={.healthy} {end}{"\n"}'
