#!/usr/bin/env bash
# Point the Connected spoke at the foundation-runtime plane and watch the hub install Flux
# and vela-core on it. Prints a line a minute for up to 15 minutes, stops when the
# SpokeCluster reports Bootstrapped=True, exits 1 on timeout.
#
# Spoke reads go through the hub's cluster-gateway proxy (kubectl get --raw .../clustergateways/
# $SPOKE/proxy/...). A direct spoke kubeconfig is not an option by default: the operator's
# role (checked 2026-10-11, gwre-insurer_operators_admin) has no EKS access entry on
# $EKS_NAME; only the CAPA, node and hub spoke roles do. See 11-bootstrap-proof.sh for the
# SPOKE_KC override when an access entry exists.
set -euo pipefail
source "$(dirname "$0")/env.sh"
export KUBECONFIG=$HUB_KC
kubectl() { command kubectl --request-timeout=15s "$@"; }   # a dead network must fail fast, not hang the wait loop

SC=spokeclusters.core.oam.dev
APP=applications.core.oam.dev
PLANE=foundation-runtime
PROXY=/apis/cluster.core.oam.dev/v1alpha1/clustergateways/$SPOKE/proxy
spoke_get() { kubectl get --raw "$PROXY$1" 2>/dev/null || true; }   # empty output when the object or the proxy is not there yet
sc_field() { kubectl get $SC $SPOKE -n $NS -o jsonpath="$1" 2>/dev/null || echo "?"; }
cond_status() { sc_field "{.status.conditions[?(@.type==\"$1\")].status}"; }
cond_reason() { sc_field "{.status.conditions[?(@.type==\"$1\")].reason}"; }
cond_message() { sc_field "{.status.conditions[?(@.type==\"$1\")].message}"; }
conditions() { sc_field '{range .status.conditions[*]}  {.type}={.status} ({.reason}) {.message}{"\n"}{end}'; }
bootstrap_app() { local a; a=$(sc_field '{.status.bootstrap.applicationName}'); [ -n "$a" ] && [ "$a" != "?" ] && echo "$a" || echo "sc-$SPOKE-bootstrap"; }
hr_ready() { # Ready status and message of the spoke's HelmRelease vela-core, "absent" until Flux has it
  spoke_get /apis/helm.toolkit.fluxcd.io/v2/namespaces/$NS/helmreleases/vela-core \
    | jq -r '.status.conditions[]? | select(.type=="Ready") | "\(.status) \(.message)"' 2>/dev/null | cut -c1-120 | grep . || echo absent
}
spoke_pods() { # name=phase for every pod in a spoke namespace, replicaset and pod hashes stripped
  spoke_get /api/v1/namespaces/$1/pods \
    | jq -r '[.items[]? | "\(.metadata.name | sub("-[a-z0-9]{8,10}-[a-z0-9]{5}$"; ""))=\(.status.phase)"] | join(" ")' 2>/dev/null | grep . || echo none
}

kubectl get $SC $SPOKE -n $NS   # bare call first so a missing kubeconfig or spoke shows its real error
kubectl get clusterplanes.core.oam.dev $PLANE -n $NS -o name   # the plane must already be applied (see the register)
[ "$(sc_field '{.status.connection}')" = Connected ] || { echo "spoke is not Connected; bootstrap needs a reachable spoke"; exit 1; }

kubectl patch $SC $SPOKE -n $NS --type merge -p '{"spec":{"bootstrap":{"planeRef":{"name":"foundation-runtime"}}}}'
echo "REGISTER NOW: Application sc-$SPOKE-bootstrap (creating), Flux and vela-core on $EKS_NAME (creating)"

status=?
for i in $(seq 0 15); do
  [ $i -gt 0 ] && sleep 60
  status=$(cond_status Bootstrapped); reason=$(cond_reason Bootstrapped)
  app=$(bootstrap_app)
  appphase=$(kubectl get $APP $app -n $NS -o jsonpath='{.status.status}' 2>/dev/null | grep . || echo absent)
  echo "$(date -u +%H:%M) Bootstrapped=${status:-absent} (${reason:-none}) bootstrap.phase=$(sc_field '{.status.bootstrap.phase}') app=$app/$appphase"
  echo "       message: $(cond_message Bootstrapped | cut -c1-200)"
  echo "       spoke: hr.Ready=$(hr_ready) | flux-system: $(spoke_pods flux-system) | vela-system: $(spoke_pods vela-system)"
  [ "$status" = True ] && break
done

echo "== conditions:"; conditions
if [ "$status" != True ]; then echo "TIMEOUT: Bootstrapped is not True after 15 minutes"; exit 1; fi
kubectl get $APP $(bootstrap_app) -n $NS -o jsonpath='{.metadata.name} {.status.status} {range .status.services[*]}{.name}={.healthy} {end}{"\n"}' || true
echo "spoke vela-core image: $(spoke_get /apis/apps/v1/namespaces/$NS/deployments/vela-core | jq -r '.spec.template.spec.containers[0].image // "?"')"
