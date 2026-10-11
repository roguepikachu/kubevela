#!/usr/bin/env bash
# Live proof of bootstrap on a Connected, Bootstrapped spoke, in three steps:
#   1 repair: delete the HelmRelease on the spoke, the hub puts it back and it goes Ready again
#   2 version as a parameter: bump the plane's vela component to a second chart and image
#     already in ECR, the spoke's vela-core rolls to it, then roll back
#   3 hub down: with the hub controllers scaled to 0 the spoke runtime stays up on its own
# Safe to re-run from a Connected spoke with Bootstrapped=True. kubectl only; aws is read-only here.
#
# Inputs: CHART_TAG2 and IMAGE_TAG2 (step 2), the TAG suffixes of a second chart
# (0.1.0-cp.$CHART_TAG2 in $PREFIX/vela-core, pushed by CHART_TAG=... 08-chart.sh) and a second
# image ($REPO:$IMAGE_TAG2, pushed by 01-image.sh from another commit). Both are checked up
# front with describe-images so the proof does not half-run.
#
# How the spoke is reached. Decision made 2026-10-11 after the read-only checks:
#   - The operator's role (gwre-insurer_operators_admin) has NO EKS access entry on $EKS_NAME;
#     only AWSServiceRoleForAmazonEKS, the CAPA role, the node role and the hub's spoke role do.
#     So `aws eks update-kubeconfig` would yield a kubeconfig that cannot authenticate.
#   - kubectl v1.37.1 supports `kubectl delete --raw`, so the one delete this proof needs (the
#     HelmRelease in step 1) goes through the hub's cluster-gateway proxy, like every read.
#   - Default therefore: all spoke reads and the delete via
#     /apis/cluster.core.oam.dev/v1alpha1/clustergateways/$SPOKE/proxy on the hub kubeconfig.
#   - Consequence for step 3: scaling vela-core-cluster-core (the gateway) to 0 removes that
#     read path. Without a direct kubeconfig the step scales only vela-core (the controller) to 0
#     and keeps the gateway, a stateless proxy with no reconcile loop, up as the read path.
#     Set SPOKE_KC=/path/to/kubeconfig (built after creating an access entry for the operator,
#     commands printed in step 0) to run the full variant with both hub deployments at 0.
#     The verdict line states which variant ran.
set -Eeuo pipefail   # -E so the ERR trap also fires for a wait_for timeout inside a helper function
source "$(dirname "$0")/env.sh"
export KUBECONFIG=$HUB_KC
kubectl() { command kubectl --request-timeout=15s "$@"; }   # a dead network must fail fast, not hang the wait loops

SC=spokeclusters.core.oam.dev
APP=applications.core.oam.dev
PLANE=foundation-runtime
CHART_REPO=$PREFIX/vela-core
PROXY=/apis/cluster.core.oam.dev/v1alpha1/clustergateways/$SPOKE/proxy
HR_PATH=/apis/helm.toolkit.fluxcd.io/v2/namespaces/$NS/helmreleases/vela-core
SPOKE_KC=${SPOKE_KC:-}
HUB_DOWN=controller; [ -n "$SPOKE_KC" ] && HUB_DOWN=full

# Spoke access. One switch: SPOKE_KC set means direct kubeconfig, otherwise the gateway proxy.
spoke_get() { # API path; empty output when the object is not there, never aborts the script
  if [ -n "$SPOKE_KC" ]; then command kubectl --kubeconfig $SPOKE_KC --request-timeout=15s get --raw "$1" 2>/dev/null || true
  else kubectl get --raw "$PROXY$1" 2>/dev/null || true; fi
}
spoke_delete() { # API path; the one write this proof sends to the spoke
  if [ -n "$SPOKE_KC" ]; then command kubectl --kubeconfig $SPOKE_KC --request-timeout=15s delete --raw "$1"
  else kubectl delete --raw "$PROXY$1"; fi
}

sc_field() { kubectl get $SC $SPOKE -n $NS -o jsonpath="$1" 2>/dev/null || echo "?"; }
cond_status() { sc_field "{.status.conditions[?(@.type==\"$1\")].status}"; }
cond_reason() { sc_field "{.status.conditions[?(@.type==\"$1\")].reason}"; }
cond_message() { sc_field "{.status.conditions[?(@.type==\"$1\")].message}"; }
conditions() { sc_field '{range .status.conditions[*]}{.type}={.status} ({.reason}){"\n"}{end}'; }
hr_json() { spoke_get $HR_PATH; }
hr_uid() { hr_json | jq -r '.metadata.uid // empty' 2>/dev/null | grep . || true; }
hr_ready() { hr_json | jq -r '.status.conditions[]? | select(.type=="Ready") | .status' 2>/dev/null | grep . || echo absent; }
hr_chart_version() { hr_json | jq -r '.status.history[0].chartVersion // .spec.chart.spec.version // "?"' 2>/dev/null | grep . || echo "?"; }   # jq on empty stdin exits 0 silently, hence grep
spoke_image() { spoke_get /apis/apps/v1/namespaces/$NS/deployments/vela-core | jq -r '.spec.template.spec.containers[0].image // "absent"' 2>/dev/null | grep . || echo absent; }
spoke_ready_replicas() { spoke_get /apis/apps/v1/namespaces/$1/deployments/$2 | jq -r '.status.readyReplicas // 0' 2>/dev/null | grep . || echo 0; }
vela_prop() { # a property of the plane's vela-core component, dotted path
  kubectl get clusterplanes.core.oam.dev $PLANE -n $NS -o json | jq -r --arg p "$1" '.spec.components[] | select(.name=="vela-core") | .properties | getpath($p | split(".")) // "?"'
}
wait_for() { # description, max seconds, command that exits 0 when satisfied; a progress line every minute
  local desc=$1 max=$2; shift 2; local t=0
  until "$@"; do
    sleep 10; t=$((t+10))
    [ $t -ge $max ] && { echo "TIMEOUT after ${max}s waiting for: $desc"; return 1; }
    [ $((t % 60)) -eq 0 ] && echo "  $(date -u +%H:%M) ${t}s: Bootstrapped=$(cond_status Bootstrapped) ($(cond_reason Bootstrapped)) hr.Ready=$(hr_ready) image=$(spoke_image)"
  done
  echo "ok ($t s): $desc"
}
step() { echo; echo "== $(date -u +%H:%M:%S) $*"; }
HUB_SCALED=0   # 1 while step 3 has the hub scaled down; both traps below put the replicas back
R_CORE=1; R_GW=2   # replaced in step 0 by the deployments' own spec.replicas
restore_hub() { echo "restoring hub deployments (vela-core=$R_CORE vela-core-cluster-core=$R_GW)"; kubectl scale deploy vela-core -n $NS --replicas=$R_CORE || echo "MANUAL: kubectl scale deploy vela-core -n $NS --replicas=$R_CORE"; kubectl scale deploy vela-core-cluster-core -n $NS --replicas=$R_GW || echo "MANUAL: kubectl scale deploy vela-core-cluster-core -n $NS --replicas=$R_GW"; HUB_SCALED=0; }   # each scale tolerates failure so the second one still runs inside the trap
trap 'echo "== aborted; last known state:"; conditions || true; kubectl get $SC -n $NS || true' ERR
trap '[ "${HUB_SCALED:-0}" = 1 ] && restore_hub' EXIT   # Ctrl-C, explicit exit and ERR all end here
FAILS=0
expect() { # description, actual, wanted; records a mismatch instead of aborting so the run completes
  if [ "$2" = "$3" ]; then echo "ok: $1 = '$2'"; else echo "FAIL: $1 = '$2' (want '$3')"; FAILS=$((FAILS+1)); fi
}

# Predicates for wait_for. Each exits 0 when satisfied and never aborts the script.
bootstrapped() { [ "$(cond_status Bootstrapped)" = True ]; }
connected() { [ "$(sc_field '{.status.connection}')" = Connected ]; }
hr_recreated() { local u; u=$(hr_uid); [ -n "$u" ] && [ "$u" != "$UID0" ] && [ "$(hr_ready)" = True ]; }
image_is() { case "$(spoke_image)" in *":$1") return 0;; *) return 1;; esac; }
image_is_new() { image_is "$IMAGE_TAG2"; }
image_is_old() { image_is "$IMG0"; }

patch_vela() { # chart version, image tag of the vela-core component. JSON merge replaces arrays whole, so python rebuilds the components array
  # The patch carries the whole components array (about 200 KB of Flux objects), far beyond the argv
  # limit that made `-p "$patch"` fail with "Argument list too long" on 2026-10-11, so it goes through a file.
  local patch_file=$OUT/plane-patch.json
  kubectl get clusterplanes.core.oam.dev $PLANE -n $NS -o json | python3 -c '
import json, sys
plane = json.load(sys.stdin); ver, img = sys.argv[1], sys.argv[2]
comps = plane["spec"]["components"]
vela = [c for c in comps if c["name"] == "vela-core"]
if len(vela) != 1: sys.exit("plane has %d components named vela-core" % len(vela))
props = vela[0].setdefault("properties", {})
props["version"] = ver
props.setdefault("values", {}).setdefault("image", {})["tag"] = img
print(json.dumps({"spec": {"components": comps}}))
' "$1" "$2" > "$patch_file"
  kubectl patch clusterplanes.core.oam.dev $PLANE -n $NS --type merge --patch-file "$patch_file"
}
spoke_runtime_ready() { # yes when all four runtime deployments have at least one ready replica
  local d ok=yes
  for d in vela-system/vela-core vela-system/vela-core-cluster-core flux-system/source-controller flux-system/helm-controller; do
    [ "$(spoke_ready_replicas ${d%/*} ${d#*/})" -ge 1 ] || ok=no
  done
  echo $ok
}

step "0 starting state and preflight"
kubectl get $SC $SPOKE -n $NS   # bare call first so a missing kubeconfig or spoke shows its real error
kubectl get clusterplanes.core.oam.dev $PLANE -n $NS -o name
echo "connection=$(sc_field '{.status.connection}') Bootstrapped=$(cond_status Bootstrapped) ($(cond_reason Bootstrapped)) app=$(sc_field '{.status.bootstrap.applicationName}')"
if ! connected || ! bootstrapped; then echo "expected a Connected spoke with Bootstrapped=True; run 10-bootstrap.sh first"; exit 1; fi
[ -n "${CHART_TAG2:-}" ] && [ -n "${IMAGE_TAG2:-}" ] || {
  echo "step 2 needs CHART_TAG2 and IMAGE_TAG2: the TAG suffix of a second chart (0.1.0-cp.<CHART_TAG2> in $CHART_REPO) and a second image tag in $REPO."
  echo "publish them first, e.g. CHART_TAG=<tag> $POC/scripts/08-chart.sh from a worktree at another commit"; exit 2
}
aws ecr describe-images --repository-name $CHART_REPO --region $REGION --image-ids imageTag=0.1.0-cp.$CHART_TAG2 >/dev/null 2>&1 || { echo "chart $CHART_REPO:0.1.0-cp.$CHART_TAG2 is not in ECR"; exit 2; }
aws ecr describe-images --repository-name $REPO --region $REGION --image-ids imageTag=$IMAGE_TAG2 >/dev/null 2>&1 || { echo "image $REPO:$IMAGE_TAG2 is not in ECR"; exit 2; }
if [ -z "$SPOKE_KC" ]; then
  kubectl delete --help | grep -q -- --raw || {
    echo "this kubectl has no 'delete --raw', so the HelmRelease cannot be deleted through the gateway proxy."
    echo "either upgrade kubectl (v1.37.1 has it) or create an access entry for your role and set SPOKE_KC; see the commands below"; exit 2
  }
  echo "spoke access: gateway proxy (reads and the HelmRelease delete); step 3 runs the controller-only variant"
else
  echo "spoke access: direct kubeconfig $SPOKE_KC; step 3 runs the full variant (controller and gateway at 0)"
fi
echo "to enable the full step 3 variant: run scripts/16-spoke-access.sh (one-time, AWS writes, idempotent), then export SPOKE_KC=/tmp/cp-spoke.kubeconfig"
R_CORE=$(kubectl get deploy vela-core -n $NS -o jsonpath='{.spec.replicas}'); R_GW=$(kubectl get deploy vela-core-cluster-core -n $NS -o jsonpath='{.spec.replicas}')
[ "${R_CORE:-0}" -ge 1 ] && [ "${R_GW:-0}" -ge 1 ] || { echo "hub deployments have replicas vela-core=$R_CORE vela-core-cluster-core=$R_GW; expected both >= 1 before the proof"; exit 1; }
echo "hub replicas to restore after step 3: vela-core=$R_CORE vela-core-cluster-core=$R_GW"
VER0=$(vela_prop version); IMG0=$(vela_prop values.image.tag)
echo "plane vela: version=$VER0 image.tag=$IMG0"
[ "$VER0" != "?" ] && [ "$IMG0" != "?" ] || { echo "plane component vela has no version or values.image.tag; check the plane manifest"; exit 1; }
[ "$IMG0" != "$IMAGE_TAG2" ] || { echo "IMAGE_TAG2 equals the current image tag; the roll in step 2 would be invisible"; exit 2; }
UID0=$(hr_uid)
[ -n "$UID0" ] || { echo "cannot read HelmRelease vela-core on the spoke through the chosen access path"; exit 1; }
echo "hr.uid=$UID0 hr.Ready=$(hr_ready) hr.chart=$(hr_chart_version) image=$(spoke_image)"

step "1 repair: delete the HelmRelease on the spoke, the hub must put it back"
spoke_delete $HR_PATH
wait_for "HelmRelease vela-core recreated (uid != $UID0) and Ready" 480 hr_recreated
wait_for "Bootstrapped True again" 480 bootstrapped
echo "hr.uid=$(hr_uid) hr.Ready=$(hr_ready) image=$(spoke_image)"
expect "HelmRelease Ready after repair" "$(hr_ready)" True
expect "Bootstrapped reason after repair" "$(cond_reason Bootstrapped)" Bootstrapped

step "2 version as a parameter: bump the plane's vela component, then roll back"
echo "plane vela: version=$VER0 image.tag=$IMG0 -> version=0.1.0-cp.$CHART_TAG2 image.tag=$IMAGE_TAG2"
patch_vela 0.1.0-cp.$CHART_TAG2 $IMAGE_TAG2
wait_for "spoke vela-core image ends with :$IMAGE_TAG2" 600 image_is_new
wait_for "Bootstrapped True on the new version" 600 bootstrapped
echo "hr.chart=$(hr_chart_version) image=$(spoke_image)"
expect "HelmRelease Ready on the new version" "$(hr_ready)" True
patch_vela $VER0 $IMG0
wait_for "spoke vela-core image back to :$IMG0" 600 image_is_old
wait_for "Bootstrapped True after rollback" 600 bootstrapped
echo "hr.chart=$(hr_chart_version) image=$(spoke_image)"
expect "plane version restored" "$(vela_prop version)" "$VER0"

step "3 hub down ($HUB_DOWN): the spoke runtime must stay up without the hub"
HUB_SCALED=1
if [ "$HUB_DOWN" = full ]; then kubectl scale deploy vela-core vela-core-cluster-core -n $NS --replicas=0
else kubectl scale deploy vela-core -n $NS --replicas=0; echo "note: vela-core-cluster-core (the gateway) stays up as the only read path to the spoke; it reconciles nothing"; fi
kubectl get deploy vela-core vela-core-cluster-core -n $NS
for t in 0 30 60 90 120 150; do
  [ $t -gt 0 ] && sleep 30
  expect "t=${t}s all four spoke runtime deployments ready (vela-core, vela-core-cluster-core, source-controller, helm-controller)" "$(spoke_runtime_ready)" yes
done
echo "hr.Ready=$(hr_ready) image=$(spoke_image) (read while the hub $HUB_DOWN was down)"
restore_hub
command kubectl rollout status deploy/vela-core -n $NS --timeout=180s   # command: the 15 s request-timeout wrapper would cut the watch
command kubectl rollout status deploy/vela-core-cluster-core -n $NS --timeout=180s
wait_for "Connected after the hub is back" 300 connected
wait_for "Bootstrapped True after the hub is back" 300 bootstrapped

step "4 final"
kubectl get $SC -n $NS || true
conditions
echo "hr.uid=$(hr_uid) hr.Ready=$(hr_ready) hr.chart=$(hr_chart_version) image=$(spoke_image)"
if [ $FAILS -gt 0 ]; then echo "$FAILS expectation(s) failed"; exit 1; fi
echo "bootstrap proven (step 3 variant: $HUB_DOWN)"
