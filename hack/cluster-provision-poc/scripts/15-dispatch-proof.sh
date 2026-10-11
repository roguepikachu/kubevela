#!/usr/bin/env bash
# Live proof of blueprint dispatch on a Connected spoke with SpokeReady=True, in three steps:
#   1 tamper: delete the foundation-certs ClusterPlaneRevision on the spoke; the hub's vela-core
#     state-keep puts it back and the spoke Cluster stays or returns Ready
#   2 self-heal with the hub fully down: hub vela-core and vela-core-cluster-core at 0, delete the
#     cert-manager HelmRelease on the spoke; the spoke's own vela-core recreates it from the
#     plane Application, no hub involved; then bring the hub back and see it reconnect
#   3 blueprint change: give plane foundation-certs a dependsOn on foundation-policy; the hub cuts a
#     new revision, the spoke Application cluster-foundation-certs gets a depends-on-app step; revert
# Safe to re-run from SpokeReady=True. kubectl only; aws is not called.
#
# Needs SPOKE_KC: step 2 scales the hub's cluster-gateway to 0, which removes the proxy read path, so
# every spoke read and delete here uses the direct kubeconfig that 16-spoke-access.sh builds.
set -Eeuo pipefail   # -E so the ERR trap also fires for a wait_for timeout inside a helper function
source "$(dirname "$0")/env.sh"
export KUBECONFIG=$HUB_KC
kubectl() { command kubectl --request-timeout=15s "$@"; }   # a dead network must fail fast, not hang the wait loops

SC=spokeclusters.core.oam.dev
APP=applications.core.oam.dev
BLUEPRINT=cpspoke1-platform
CERTS_PLANE=foundation-certs
CERTS_APP=cluster-$CERTS_PLANE
HR_NAME=${HR_NAME:-cert-manager}   # helm-flux names the HelmRelease after the component; step 0 falls back to a *cert-manager match
SPOKE_KC=${SPOKE_KC:-}

[ -n "$SPOKE_KC" ] || { echo "SPOKE_KC is not set. One-time setup (AWS writes, idempotent): scripts/16-spoke-access.sh, then export SPOKE_KC=/tmp/cp-spoke.kubeconfig"; exit 2; }
skc() { command kubectl --kubeconfig $SPOKE_KC --request-timeout=15s "$@"; }
spoke_get() { skc get --raw "$1" 2>/dev/null || true; }   # API path; empty output when the object is not there, never aborts the script
spoke_delete() { skc delete --raw "$1"; }

sc_field() { kubectl get $SC $SPOKE -n $NS -o jsonpath="$1" 2>/dev/null || echo "?"; }
cond_status() { sc_field "{.status.conditions[?(@.type==\"$1\")].status}"; }
cond_reason() { sc_field "{.status.conditions[?(@.type==\"$1\")].reason}"; }
conditions() { sc_field '{range .status.conditions[*]}{.type}={.status} ({.reason}){"\n"}{end}'; }
revision() { sc_field '{.status.dispatchedRevision}'; }
CI_VER=v1beta1   # replaced in step 0 by the spoke's served version of core.oam.dev
cluster_json() { # the spoke's Cluster CR; cluster-scoped first, then namespaced in vela-system
  local j; j=$(spoke_get /apis/core.oam.dev/$CI_VER/clusters/$SPOKE)
  [ -n "$j" ] || j=$(spoke_get /apis/core.oam.dev/$CI_VER/namespaces/$NS/clusters/$SPOKE)
  echo "$j"
}
cluster_phase() { cluster_json | jq -r '.status.phase // empty' 2>/dev/null | grep . || echo absent; }
cluster_planes() { cluster_json | jq -r '.status.planes[]? | "  plane \(.name): phase=\(.phase // "?") healthy=\(.healthy // "?") \(.message // "")"' 2>/dev/null | grep . || echo "  (no status.planes)"; }
cpr_json() { # the spoke's ClusterPlaneRevision for the certs plane: the one the dispatched ClusterBlueprintRevision references, else the newest by creationTimestamp; namespaced, then cluster-scoped
  local p want j
  for p in "/apis/core.oam.dev/$CI_VER/namespaces/$NS" "/apis/core.oam.dev/$CI_VER"; do
    want=$(spoke_get "$p/clusterblueprintrevisions/$(revision)" | jq -r --arg n "$CERTS_PLANE" '.spec.planes[]? | select(.name==$n) | .planeRevision // empty' 2>/dev/null | grep . || true)
    if [ -n "$want" ]; then j=$(spoke_get "$p/clusterplanerevisions/$want" | jq -c 'select(.metadata)' 2>/dev/null); [ -n "$j" ] && { echo "$j"; return; }; fi
    j=$(spoke_get "$p/clusterplanerevisions" | jq -c --arg n "$CERTS_PLANE" '[.items[]? | select(.spec.planeName==$n or .metadata.labels["core.oam.dev/plane-name"]==$n)] | sort_by(.metadata.creationTimestamp) | last // empty' 2>/dev/null)
    [ -n "$j" ] && { echo "$j"; return; }
  done
}
cpr_uid() { cpr_json | jq -r '.metadata.uid // empty' 2>/dev/null | grep . || true; }
cpr_name() { cpr_json | jq -r '.metadata.name // empty' 2>/dev/null | grep . || true; }
cpr_path() { cpr_json | jq -r --arg v "$CI_VER" 'if .metadata.namespace then "/apis/core.oam.dev/\($v)/namespaces/\(.metadata.namespace)/clusterplanerevisions/\(.metadata.name)" else "/apis/core.oam.dev/\($v)/clusterplanerevisions/\(.metadata.name)" end' 2>/dev/null | grep . || true; }
hr_path() { echo /apis/helm.toolkit.fluxcd.io/v2/namespaces/$NS/helmreleases/$HR_NAME; }
hr_json() { spoke_get "$(hr_path)"; }
hr_uid() { hr_json | jq -r '.metadata.uid // empty' 2>/dev/null | grep . || true; }
hr_ready() { hr_json | jq -r '.status.conditions[]? | select(.type=="Ready") | .status' 2>/dev/null | grep . || echo absent; }   # jq on empty stdin exits 0 silently, hence grep
certs_app_steps() { spoke_get /apis/core.oam.dev/v1beta1/namespaces/$NS/applications/$CERTS_APP | jq -r '[.spec.workflow.steps[]?.type] | join(",")' 2>/dev/null | grep . || echo none; }
wait_for() { # description, max seconds, command that exits 0 when satisfied; a progress line every minute
  local desc=$1 max=$2; shift 2; local t=0
  until "$@"; do
    sleep 10; t=$((t+10))
    [ $t -ge $max ] && { echo "TIMEOUT after ${max}s waiting for: $desc"; return 1; }
    [ $((t % 60)) -eq 0 ] && echo "  $(date -u +%H:%M) ${t}s: SpokeReady=$(cond_status SpokeReady) ($(cond_reason SpokeReady)) Dispatched=$(cond_status Dispatched) revision=$(revision) Cluster.phase=$(cluster_phase) hr.Ready=$(hr_ready)"
  done
  echo "ok ($t s): $desc"
}
step() { echo; echo "== $(date -u +%H:%M:%S) $*"; }
HUB_SCALED=0   # 1 while step 2 has the hub scaled down; both traps below put the replicas back
R_CORE=1; R_GW=2   # replaced in step 0 by the deployments' own spec.replicas
restore_hub() { echo "restoring hub deployments (vela-core=$R_CORE vela-core-cluster-core=$R_GW)"; kubectl scale deploy vela-core -n $NS --replicas=$R_CORE || echo "MANUAL: kubectl scale deploy vela-core -n $NS --replicas=$R_CORE"; kubectl scale deploy vela-core-cluster-core -n $NS --replicas=$R_GW || echo "MANUAL: kubectl scale deploy vela-core-cluster-core -n $NS --replicas=$R_GW"; HUB_SCALED=0; }   # each scale tolerates failure so the second one still runs inside the trap
trap 'echo "== aborted; last known state:"; conditions || true; kubectl get $SC -n $NS || true; cluster_planes || true' ERR
trap '[ "${HUB_SCALED:-0}" = 1 ] && restore_hub' EXIT   # Ctrl-C, explicit exit and ERR all end here
FAILS=0
expect() { # description, actual, wanted; records a mismatch instead of aborting so the run completes
  if [ "$2" = "$3" ]; then echo "ok: $1 = '$2'"; else echo "FAIL: $1 = '$2' (want '$3')"; FAILS=$((FAILS+1)); fi
}

# Predicates for wait_for. Each exits 0 when satisfied and never aborts the script.
spoke_ready() { [ "$(cond_status SpokeReady)" = True ]; }
dispatched() { [ "$(cond_status Dispatched)" = True ]; }
connected() { [ "$(sc_field '{.status.connection}')" = Connected ]; }
hub_all_true() { connected && dispatched && spoke_ready; }
cluster_ready() { [ "$(cluster_phase)" = Ready ]; }
cpr_recreated() { local u; u=$(cpr_uid); [ -n "$u" ] && [ "$u" != "$CPR_UID0" ]; }
hr_recreated() { local u; u=$(hr_uid); [ -n "$u" ] && [ "$u" != "$HR_UID0" ] && [ "$(hr_ready)" = True ]; }
revision_not() { local r; r=$(revision); [ -n "$r" ] && [ "$r" != "?" ] && [ "$r" != "$1" ]; }
certs_app_depends() { case ",$(certs_app_steps)," in *,depends-on-app,*) return 0;; *) return 1;; esac; }
patch_blueprint() { # add|remove: set or clear dependsOn [foundation-policy] on plane foundation-certs. JSON merge replaces arrays whole, so python rebuilds the planes array
  kubectl get clusterblueprints.core.oam.dev $BLUEPRINT -n $NS -o json > "$OUT/blueprint-current.json"
  python3 -I - "$OUT/blueprint-current.json" "$OUT/blueprint-patch.json" "$1" "$CERTS_PLANE" <<'PY'
import json, sys
src, out, mode, plane = sys.argv[1:5]
with open(src) as f:
    planes = json.load(f)["spec"]["planes"]
hits = [p for p in planes if p.get("name") == plane]
if len(hits) != 1:
    sys.exit("blueprint has %d planes named %s" % (len(hits), plane))
if mode == "add":
    hits[0]["dependsOn"] = ["foundation-policy"]
else:
    hits[0].pop("dependsOn", None)
with open(out, "w") as f:
    json.dump({"spec": {"planes": planes}}, f)
PY
  kubectl patch clusterblueprints.core.oam.dev $BLUEPRINT -n $NS --type merge --patch-file "$OUT/blueprint-patch.json"
}

step "0 starting state and preflight"
kubectl get $SC $SPOKE -n $NS   # bare call first so a missing kubeconfig or spoke shows its real error
skc get ns >/dev/null 2>&1 || { echo "SPOKE_KC=$SPOKE_KC cannot reach the spoke. One-time setup (AWS writes, idempotent): scripts/16-spoke-access.sh, then export SPOKE_KC=/tmp/cp-spoke.kubeconfig"; exit 2; }
echo "spoke access: direct kubeconfig $SPOKE_KC"
CI_VER=$(spoke_get /apis/core.oam.dev | jq -r '.preferredVersion.version // empty' 2>/dev/null | grep . || echo v1beta1)
echo "spoke cluster-infrastructure API version: $CI_VER"
conditions
[ "$(cond_status SpokeReady)" = True ] && [ "$(cond_status Dispatched)" = True ] || { echo "preflight needs SpokeReady=True and Dispatched=True (run 14-dispatch.sh first)"; exit 1; }
R_CORE=$(kubectl get deploy vela-core -n $NS -o jsonpath='{.spec.replicas}'); R_GW=$(kubectl get deploy vela-core-cluster-core -n $NS -o jsonpath='{.spec.replicas}')
[ "${R_CORE:-0}" -ge 1 ] && [ "${R_GW:-0}" -ge 1 ] || { echo "hub deployments have replicas vela-core=$R_CORE vela-core-cluster-core=$R_GW; expected both >= 1 before the proof"; exit 1; }
echo "hub replicas to restore after step 2: vela-core=$R_CORE vela-core-cluster-core=$R_GW"
if [ -z "$(hr_uid)" ]; then   # the rendered component may carry the plane prefix; take the one HelmRelease whose name ends with cert-manager
  found=$(spoke_get /apis/helm.toolkit.fluxcd.io/v2/namespaces/$NS/helmreleases | jq -r '[.items[]? | .metadata.name | select(endswith("cert-manager"))] | join(" ")' 2>/dev/null | grep . || true)
  [ "$(echo $found | wc -w)" -eq 1 ] || { echo "cannot find one HelmRelease named $HR_NAME or *cert-manager in $NS on the spoke (found: '${found:-none}'); set HR_NAME"; exit 1; }
  echo "HelmRelease is $found, not $HR_NAME; using that"; HR_NAME=$found
fi
echo "hr=$HR_NAME uid=$(hr_uid) Ready=$(hr_ready) | Cluster.phase=$(cluster_phase) | $CERTS_APP steps=$(certs_app_steps)"
cluster_planes
REV=$(revision); [ -n "$REV" ] && [ "$REV" != "?" ] || { echo "status.dispatchedRevision is empty"; exit 1; }
echo "dispatched revision: $REV"

step "1 tamper: delete the $CERTS_PLANE ClusterPlaneRevision on the spoke, the hub must put it back"
CPR_UID0=$(cpr_uid); CPR_PATH0=$(cpr_path)
[ -n "$CPR_UID0" ] || { echo "no ClusterPlaneRevision for plane $CERTS_PLANE on the spoke (label core.oam.dev/plane-name or spec.planeName)"; exit 1; }
echo "revision $(cpr_name) uid=$CPR_UID0 at $CPR_PATH0"
spoke_delete "$CPR_PATH0" >/dev/null
wait_for "ClusterPlaneRevision for $CERTS_PLANE recreated on the spoke (uid != $CPR_UID0)" 420 cpr_recreated
wait_for "spoke Cluster phase Ready after the tamper" 420 cluster_ready
echo "revision $(cpr_name) uid=$(cpr_uid)"
expect "SpokeReady after tamper" "$(cond_status SpokeReady)" True

step "2 self-heal with the hub fully down: hub controllers at 0, delete the $HR_NAME HelmRelease on the spoke"
HR_UID0=$(hr_uid); [ -n "$HR_UID0" ] || { echo "HelmRelease $HR_NAME not readable on the spoke"; exit 1; }
HUB_SCALED=1
kubectl scale deploy vela-core vela-core-cluster-core -n $NS --replicas=0
kubectl get deploy vela-core vela-core-cluster-core -n $NS
spoke_delete "$(hr_path)" >/dev/null
echo "deleted HelmRelease $HR_NAME (uid $HR_UID0) with the hub down"
wait_for "HelmRelease $HR_NAME recreated by the spoke's own vela-core (uid != $HR_UID0) and Ready" 420 hr_recreated
wait_for "spoke Cluster phase back to Ready with the hub down" 420 cluster_ready
echo "hr.uid=$(hr_uid) hr.Ready=$(hr_ready) Cluster.phase=$(cluster_phase) (read while the hub was down)"
expect "HelmRelease Ready after self-heal" "$(hr_ready)" True
restore_hub
command kubectl rollout status deploy/vela-core -n $NS --timeout=180s   # command: the 15 s request-timeout wrapper would cut the watch
command kubectl rollout status deploy/vela-core-cluster-core -n $NS --timeout=180s
wait_for "Connected, Dispatched and SpokeReady True after the hub is back" 300 hub_all_true
expect "dispatched revision unchanged by steps 1 and 2" "$(revision)" "$REV"

step "3 blueprint change: plane $CERTS_PLANE dependsOn [foundation-policy], then revert"
patch_blueprint add
wait_for "dispatchedRevision changed from $REV" 300 revision_not "$REV"
REV2=$(revision); echo "new revision: $REV2"
wait_for "spoke Application $CERTS_APP has a depends-on-app workflow step" 300 certs_app_depends
echo "$CERTS_APP steps=$(certs_app_steps)"
wait_for "SpokeReady True on the new revision" 300 spoke_ready
patch_blueprint remove
wait_for "dispatchedRevision changed from $REV2 after the revert" 300 revision_not "$REV2"
echo "reverted revision: $(revision)"
wait_for "SpokeReady True after the revert" 300 spoke_ready
echo "$CERTS_APP steps=$(certs_app_steps)"
expect "blueprint plane $CERTS_PLANE has no dependsOn after the revert" "$(kubectl get clusterblueprints.core.oam.dev $BLUEPRINT -n $NS -o json | jq -r --arg n "$CERTS_PLANE" '.spec.planes[] | select(.name==$n) | (.dependsOn // []) | length')" 0

step "4 final"
kubectl get $SC -n $NS || true
conditions
echo "dispatched revision: $(revision)"
cluster_planes
echo "hr=$HR_NAME uid=$(hr_uid) Ready=$(hr_ready) Cluster.phase=$(cluster_phase)"
if [ $FAILS -gt 0 ]; then echo "$FAILS expectation(s) failed"; exit 1; fi
echo "dispatch proven"
