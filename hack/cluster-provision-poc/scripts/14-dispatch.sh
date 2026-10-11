#!/usr/bin/env bash
# Point the bootstrapped spoke at the cpspoke1-platform blueprint and watch the hub dispatch it:
# the hub snapshots the blueprint and its planes into revisions, renders Application
# sc-$SPOKE-dispatch onto the spoke, and the spoke's own vela-cluster-core turns the Cluster CR
# into one Application per plane (cluster-foundation-policy, cluster-foundation-certs). Prints a
# line a minute for up to 20 minutes, stops when the SpokeCluster reports SpokeReady=True, exits 1
# on timeout. Needs 13-planes-clusterinit.sh applied and Bootstrapped=True from 10-bootstrap.sh.
#
# Spoke reads: SPOKE_KC set (built by 16-spoke-access.sh) means kubectl with that kubeconfig,
# otherwise the hub's cluster-gateway proxy. Both go through raw API paths so one jq parses either.
set -euo pipefail
source "$(dirname "$0")/env.sh"
export KUBECONFIG=$HUB_KC
kubectl() { command kubectl --request-timeout=15s "$@"; }   # a dead network must fail fast, not hang the wait loop

SC=spokeclusters.core.oam.dev
APP=applications.core.oam.dev
BLUEPRINT=cpspoke1-platform
PROXY=/apis/cluster.core.oam.dev/v1alpha1/clustergateways/$SPOKE/proxy
SPOKE_KC=${SPOKE_KC:-}
spoke_get() { # API path; empty output when the object or the path is not there yet, never aborts the script
  if [ -n "$SPOKE_KC" ]; then command kubectl --kubeconfig $SPOKE_KC --request-timeout=15s get --raw "$1" 2>/dev/null || true
  else kubectl get --raw "$PROXY$1" 2>/dev/null || true; fi
}
sc_field() { kubectl get $SC $SPOKE -n $NS -o jsonpath="$1" 2>/dev/null || echo "?"; }
cond_status() { sc_field "{.status.conditions[?(@.type==\"$1\")].status}"; }
cond_reason() { sc_field "{.status.conditions[?(@.type==\"$1\")].reason}"; }
cond_message() { sc_field "{.status.conditions[?(@.type==\"$1\")].message}"; }
conditions() { sc_field '{range .status.conditions[*]}  {.type}={.status} ({.reason}) {.message}{"\n"}{end}'; }
# The cluster-infrastructure group version on the spoke, from discovery, v1beta1 when the group is not served yet.
CI_VER=$(spoke_get /apis/core.oam.dev | jq -r '.preferredVersion.version // empty' 2>/dev/null | grep . || echo v1beta1)
cluster_json() { # the spoke's Cluster CR; cluster-scoped first, then namespaced in vela-system
  local j; j=$(spoke_get /apis/core.oam.dev/$CI_VER/clusters/$SPOKE)
  [ -n "$j" ] || j=$(spoke_get /apis/core.oam.dev/$CI_VER/namespaces/$NS/clusters/$SPOKE)
  echo "$j"
}
cluster_phase() { cluster_json | jq -r '.status.phase // empty' 2>/dev/null | grep . || echo absent; }
cluster_planes() { cluster_json | jq -r '.status.planes[]? | "  plane \(.name): phase=\(.phase // "?") healthy=\(.healthy // "?") \(.message // "")"' 2>/dev/null | grep . || echo "  (no status.planes)"; }
spoke_apps() { # name=status for every Application cluster-* in the spoke's vela-system
  spoke_get /apis/core.oam.dev/v1beta1/namespaces/$NS/applications \
    | jq -r '[.items[]? | select(.metadata.name | startswith("cluster-")) | "\(.metadata.name)=\(.status.status // "pending")"] | join(" ")' 2>/dev/null | grep . || echo none
}
spoke_pods() { # name=phase for every pod in a spoke namespace, replicaset and pod hashes stripped
  spoke_get /api/v1/namespaces/$1/pods \
    | jq -r '[.items[]? | "\(.metadata.name | sub("-[a-z0-9]{8,10}-[a-z0-9]{5}$"; ""))=\(.status.phase)"] | join(" ")' 2>/dev/null | grep . || echo none
}

kubectl get $SC $SPOKE -n $NS   # bare call first so a missing kubeconfig or spoke shows its real error
kubectl get clusterblueprints.core.oam.dev $BLUEPRINT -n $NS -o name   # 13-planes-clusterinit.sh must have run
[ "$(sc_field '{.status.connection}')" = Connected ] || { echo "spoke is not Connected; dispatch needs a reachable spoke"; exit 1; }
[ "$(cond_status Bootstrapped)" = True ] || { echo "Bootstrapped is not True; the spoke needs its vela runtime first (10-bootstrap.sh)"; exit 1; }
if [ -n "$SPOKE_KC" ]; then
  command kubectl --kubeconfig $SPOKE_KC --request-timeout=15s get ns >/dev/null 2>&1 || { echo "SPOKE_KC=$SPOKE_KC cannot reach the spoke. One-time setup (AWS writes, idempotent): scripts/16-spoke-access.sh, then export SPOKE_KC=/tmp/cp-spoke.kubeconfig"; exit 2; }
  echo "spoke access: direct kubeconfig $SPOKE_KC"
else echo "spoke access: cluster-gateway proxy on the hub"; fi
echo "spoke cluster-infrastructure API version: $CI_VER"

kubectl patch $SC $SPOKE -n $NS --type merge -p "{\"spec\":{\"blueprintRef\":{\"name\":\"$BLUEPRINT\"}}}"
echo "REGISTER NOW: Application sc-$SPOKE-dispatch (creating); on $EKS_NAME: Cluster/$SPOKE, Applications cluster-foundation-policy and cluster-foundation-certs, Kyverno and cert-manager (creating)"

ready=?
for i in $(seq 0 20); do
  [ $i -gt 0 ] && sleep 60
  ready=$(cond_status SpokeReady)
  echo "$(date -u +%H:%M) Dispatched=$(cond_status Dispatched) ($(cond_reason Dispatched)) revision=$(sc_field '{.status.dispatchedRevision}') SpokeReady=${ready:-absent} ($(cond_reason SpokeReady)) health.phase=$(sc_field '{.status.health.phase}') planes=$(sc_field '{.status.health.planesHealthy}')/$(sc_field '{.status.health.planesTotal}')"
  echo "       message: $(cond_message Dispatched | cut -c1-200)"
  echo "       spoke: Cluster.phase=$(cluster_phase) | apps: $(spoke_apps)"
  echo "       spoke pods: kyverno: $(spoke_pods kyverno) | cert-manager: $(spoke_pods cert-manager)"
  [ "$ready" = True ] && break
done

echo "== conditions:"; conditions
echo "== spoke Cluster/$SPOKE planes:"; cluster_planes
if [ "$ready" != True ]; then echo "TIMEOUT: SpokeReady is not True after 20 minutes"; exit 1; fi
kubectl get $APP sc-$SPOKE-dispatch -n $NS -o jsonpath='{.metadata.name} {.status.status} {range .status.services[*]}{.name}={.healthy} {end}{"\n"}' || true
echo "dispatched revision: $(sc_field '{.status.dispatchedRevision}')"
