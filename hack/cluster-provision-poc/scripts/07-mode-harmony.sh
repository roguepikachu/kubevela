#!/usr/bin/env bash
# Live proof of the mode split on a Connected provision spoke: delete with retain,
# provision refuses the retained objects, adopt claims them, release to connect,
# connect to provision is refused, adopt claims them again. Safe to re-run from a
# Connected provision spoke. Only kubectl against the hub; aws is read-only here.
set -Eeuo pipefail   # -E so the ERR trap also fires for a wait_for timeout inside a helper function
source "$(dirname "$0")/env.sh"
export KUBECONFIG=$HUB_KC
kubectl() { command kubectl --request-timeout=15s "$@"; }   # a dead network must fail fast, not hang the wait loops (run 3 step 6 hung for hours)

SC=spokeclusters.core.oam.dev
APP=applications.core.oam.dev
CAPI_CLUSTER=clusters.cluster.x-k8s.io
sc_field() { kubectl get $SC $SPOKE -n $NS -o jsonpath="$1" 2>/dev/null || echo "?"; }
cond_reason() { sc_field "{.status.conditions[?(@.type==\"$1\")].reason}"; }
cond_message() { sc_field "{.status.conditions[?(@.type==\"$1\")].message}"; }
conditions() { sc_field '{range .status.conditions[*]}{.type}={.status} ({.reason}){"\n"}{end}'; }
eks_status() { # ACTIVE etc., absent when the cluster is gone, aws-error when aws itself fails (expired credentials)
  local s; s=$(aws eks describe-cluster --region $REGION --name $EKS_NAME --query cluster.status --output text 2>/dev/null) && echo "$s" || { aws sts get-caller-identity >/dev/null 2>&1 && echo absent || echo aws-error; }
}
capi_label() { kubectl get $CAPI_CLUSTER $SPOKE -n $NS -o jsonpath='{.metadata.labels.app\.oam\.dev/name}' 2>/dev/null || echo "?"; }
APP_SEL="spokecluster.core.oam.dev/name=$SPOKE,spokecluster.core.oam.dev/role=infra"   # the Application name carries a per-incarnation UID suffix, so select by label
app_exists() { [ "$(kubectl get $APP -n $NS -l $APP_SEL -o name 2>/dev/null | wc -l)" -gt 0 ] && echo yes || echo no; }
app_policies() { kubectl get $APP -n $NS -l $APP_SEL -o jsonpath='{range .items[0].spec.policies[*]}{.type} {end}' 2>/dev/null || echo "?"; }
wait_for() { # description, max seconds, command that exits 0 when satisfied
  local desc=$1 max=$2; shift 2; local t=0
  until "$@"; do sleep 10; t=$((t+10)); [ $t -ge $max ] && { echo "TIMEOUT after ${max}s waiting for: $desc"; return 1; }; done
  echo "ok ($t s): $desc"
}
step() { echo; echo "== $(date -u +%H:%M:%S) $*"; }
trap 'echo "== aborted; last known state:"; conditions || true; kubectl get $SC -n $NS || true' ERR
FAILS=0
expect() { # description, actual, wanted; records a mismatch instead of aborting so the run completes
  if [ "$2" = "$3" ]; then echo "ok: $1 = '$2'"; else echo "FAIL: $1 = '$2' (want '$3')"; FAILS=$((FAILS+1)); fi
}

# Predicates for wait_for. Each exits 0 when satisfied and never aborts the script.
app_gone() { [ "$(app_exists)" = no ]; }
spoke_gone() { ! kubectl get $SC $SPOKE -n $NS >/dev/null 2>&1; }
label_empty() { # tripwire: a CAPI Cluster with a deletionTimestamp means retain did not hold; stop before the EKS cluster goes with it
  if [ -n "$(kubectl get $CAPI_CLUSTER $SPOKE -n $NS -o jsonpath='{.metadata.deletionTimestamp}' 2>/dev/null)" ]; then
    echo "FAIL: CAPI Cluster is being deleted; retain did not hold"; exit 1
  fi
  [ -z "$(capi_label)" ]
}
label_is_infra() { case "$(capi_label)" in "sc-$SPOKE-infra"*) return 0;; *) return 1;; esac; }   # name carries a per-incarnation UID suffix
connected() { [ "$(sc_field '{.status.connection}')" = Connected ]; }
unmanaged_exists() { [ "$(cond_reason InfraProvisioned)" = InfraUnmanagedExists ]; }
released() { [ -z "$(sc_field '{.status.provisioning.applicationName}')" ] && connected; }
adopted() { connected && label_is_infra; }

delete_spoke_retain() { # delete the SpokeCluster; retain keeps the CAPI objects and the EKS cluster
  kubectl delete $SC $SPOKE -n $NS --wait=false
  wait_for "SpokeCluster gone" 240 spoke_gone
  wait_for "infra Application gone" 120 app_gone
  wait_for "app.oam.dev/name label stripped from CAPI Cluster" 120 label_empty
  echo "capi_label=$(capi_label) eks=$(eks_status)"
  expect "EKS status after retain delete" "$(eks_status)" ACTIVE
}

step "0 starting state"
kubectl get $SC $SPOKE -n $NS   # bare call first so a missing kubeconfig or spoke shows its real error
mode=$(sc_field '{.spec.mode}'); conn=$(sc_field '{.status.connection}')
echo "mode=$mode connection=$conn eks=$(eks_status)"
if { [ "$mode" != provision ] && [ "$mode" != adopt ]; } || [ "$conn" != Connected ]; then
  echo "expected a Connected provision or adopt spoke; see the register"; exit 1
fi

step "1 delete in $mode mode (infraDeletionPolicy retain)"
delete_spoke_retain

step "2 negative: provision over retained objects must refuse"
kubectl apply -f $POC/manifests/spokecluster-$SPOKE.yaml
wait_for "InfraProvisioned reason InfraUnmanagedExists" 240 unmanaged_exists
echo "message: $(cond_message InfraProvisioned)"
kubectl get events -n $NS --field-selector involvedObject.name=$SPOKE,involvedObject.kind=SpokeCluster --sort-by=.lastTimestamp 2>/dev/null | tail -3 || true
expect "infra Application still owned by the refusing SpokeCluster" "$(app_exists)" yes
delete_spoke_retain

step "3 positive: adopt claims the retained objects"
kubectl apply -f $POC/manifests/spokecluster-$SPOKE-adopt.yaml
wait_for "Connected with CAPI Cluster labelled sc-$SPOKE-infra" 360 adopted
policies=$(app_policies)
echo "policies: $policies"
case " $policies " in *" take-over "*) echo "ok: take-over policy present";; *) echo "FAIL: take-over policy missing"; FAILS=$((FAILS+1));; esac

step "4 release: adopt to connect"
kubectl patch $SC $SPOKE -n $NS --type merge -p '{"spec":{"mode":"connect","infraProvisioning":null}}'
wait_for "status.provisioning empty and still Connected" 180 released
wait_for "infra Application gone after release" 120 app_gone
wait_for "label stripped after release" 120 label_empty
expect "EKS status after release" "$(eks_status)" ACTIVE
conditions

step "5 forbidden: connect to provision must be refused"
if kubectl patch $SC $SPOKE -n $NS --type merge -p '{"spec":{"mode":"provision","infraProvisioning":{"blueprintRef":{"name":"eks-capi"}}}}' 2>$OUT/forbidden.err; then
  echo "FAIL: connect to provision was accepted"; conditions; exit 1
else
  echo "ok: refused"; cut -c1-300 $OUT/forbidden.err
  grep -qiE 'invalid|denied|forbidden|webhook|mode' $OUT/forbidden.err || { echo "FAIL: patch failed but not with a validation error"; FAILS=$((FAILS+1)); }
fi

step "6 adopt again: connect to adopt"
kubectl patch $SC $SPOKE -n $NS --type merge -p '{"spec":{"mode":"adopt","infraProvisioning":{"blueprintRef":{"name":"eks-capi"}}}}'
wait_for "Connected with CAPI Cluster labelled sc-$SPOKE-infra" 360 adopted
echo "policies: $(app_policies)"

step "7 final"
kubectl get $SC -n $NS || true
conditions
echo "eks=$(eks_status) capi_label=$(capi_label) app_exists=$(app_exists)"
if [ $FAILS -gt 0 ]; then echo "$FAILS expectation(s) failed"; exit 1; fi
echo "mode harmony proven"
