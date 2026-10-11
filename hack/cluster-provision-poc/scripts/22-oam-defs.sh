#!/usr/bin/env bash
# Apply the cluster-infra WorkflowStepDefinitions, wait-spoke-connected and dispatch-blueprint, to
# the hub's vela-system. Same route as 09-bootstrap-defs.sh takes for helm-flux: vela def turns the
# CUE file into the definition object, here with --dry-run so the object goes through kubectl
# server-side apply with --force-conflicts under the POC's field manager. Idempotent.
set -Eeuo pipefail
source "$(dirname "$0")/env.sh"
export KUBECONFIG=$HUB_KC
STEPS=$WORKTREE/vela-templates/definitions/cluster-infra/workflowstep

apply_step() { # CUE file
  /tmp/vela def apply "$1" -n $NS --dry-run | kubectl apply --server-side --force-conflicts --field-manager=cluster-provision-poc -f -
}
apply_step $STEPS/wait-spoke-connected.cue
apply_step $STEPS/dispatch-blueprint.cue

kubectl get workflowstepdefinitions.core.oam.dev -n $NS wait-spoke-connected dispatch-blueprint
