#!/usr/bin/env bash
# Prepares a local k3d cluster for running the KubeVela controller from an IDE
# with the admission webhooks enabled.
#
# What it does:
#   1. Creates a k3d cluster and writes its kubeconfig to ~/.kube/vela-interview
#      (skip with --skip-cluster to reuse the cluster from an earlier run)
#   2. Preloads the images the sample applications use
#   3. Installs the vela-core chart with the controller scaled to 0, so the CRDs
#      and the built-in definitions (webservice, scaler, ...) exist but nothing
#      in the cluster is reconciling
#   4. Generates webhook certs into k8s-webhook-server/serving-certs and points the
#      webhook configurations at your machine (hack/debug-webhook-setup.sh)
#
# After this, start cmd/core from your IDE with the flags in
# hack/interview/launch.json.example.

set -euo pipefail

CLUSTER="${CLUSTER:-vela-interview}"
K3S_IMAGE="${K3S_IMAGE:-rancher/k3s:v1.31.5-k3s1}"
WEBHOOK_PORT="${WEBHOOK_PORT:-9445}"
SAMPLE_IMAGES=(nginx:1.27-alpine busybox:1.36)
# System images for the pinned k3s version. Importing them from the host means
# the cluster works even where the node cannot pull from Docker Hub itself
# (for example behind a TLS-intercepting proxy).
SYSTEM_IMAGES=(rancher/mirrored-pause:3.6 rancher/mirrored-coredns-coredns:1.12.0)

SKIP_CLUSTER=false
for arg in "$@"; do
  case "$arg" in
    --skip-cluster) SKIP_CLUSTER=true ;;
    *) echo "unknown argument: $arg" >&2; exit 1 ;;
  esac
done

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
# shellcheck source=hack/interview/lib.sh
source hack/interview/lib.sh

if [ "$SKIP_CLUSTER" = false ]; then
  step "Creating k3d cluster ${CLUSTER}"
  if k3d cluster list -o json | grep -q "\"name\":\"${CLUSTER}\""; then
    k3d cluster delete "${CLUSTER}"
  fi
  # traefik, metrics-server and local-storage are not needed and only cost memory
  k3d cluster create "${CLUSTER}" --image "${K3S_IMAGE}" --wait \
    --k3s-arg "--disable=traefik@server:0" \
    --k3s-arg "--disable=metrics-server@server:0" \
    --k3s-arg "--disable=local-storage@server:0" \
    --kubeconfig-update-default=false
  mkdir -p "$(dirname "${VELA_KUBECONFIG}")"
  # It holds the cluster admin key, so keep it private.
  (umask 077 && k3d kubeconfig get "${CLUSTER}" > "${VELA_KUBECONFIG}")
elif [ ! -f "${VELA_KUBECONFIG}" ]; then
  echo "no kubeconfig at ${VELA_KUBECONFIG}; run without --skip-cluster first" >&2
  exit 1
fi

kubectl cluster-info >/dev/null

step "Preloading images"
for img in "${SYSTEM_IMAGES[@]}" "${SAMPLE_IMAGES[@]}"; do
  pull_and_import "$img"
done

# Webhook configs from a previous run point at a controller that isn't running
# yet. With failurePolicy: Fail they would stop the chart applying definitions.
make webhook-clean

step "Installing vela-core chart (controller scaled to 0)"
helm upgrade --install kubevela ./charts/vela-core \
  --namespace vela-system --create-namespace \
  --set replicaCount=0 \
  --set admissionWebhooks.enabled=false \
  --set multicluster.enabled=false \
  --wait --timeout 5m

step "Pointing admission webhooks at this machine"
./hack/debug-webhook-setup.sh "${WEBHOOK_PORT}"

step "Done"
echo "Cluster:  k3d-${CLUSTER}"
echo "Next:     export KUBECONFIG=${VELA_KUBECONFIG}"
echo "          then start cmd/core from your IDE (see hack/interview/launch.json.example)"
