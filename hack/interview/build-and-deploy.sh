#!/usr/bin/env bash
# Builds the vela-core image from the working tree and runs it inside the k3d
# cluster created by hack/interview/k3d-up.sh, with the chart's own admission
# webhooks (in-cluster service + generated certs) instead of the local ones.
#
# Stop the controller in your IDE before running this.

set -euo pipefail

CLUSTER="${CLUSTER:-vela-interview}"
# A new tag per build changes the Deployment, so helm rolls the pod by itself.
TAG="${TAG:-interview-$(date +%Y%m%d%H%M%S)}"
IMAGE="vela-core:${TAG}"

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
# shellcheck source=hack/interview/lib.sh
source hack/interview/lib.sh

step "Building ${IMAGE}"
if [ "${FULL_BUILD:-false}" = true ]; then
  # Compiles inside Docker with the repository's Dockerfile. Slower, and needs
  # the Docker build to reach proxy.golang.org.
  make docker-build-core VELA_CORE_IMAGE="${IMAGE}"
else
  # Compiles on the host (reusing the Go build cache) and packages the binary.
  CTX="bin/interview-image"
  mkdir -p "${CTX}"
  CGO_ENABLED=0 GOOS=linux GOARCH="${DOCKER_ARCH}" \
    go build -ldflags "-s -w -X github.com/oam-dev/kubevela/version.VelaVersion=interview -X github.com/oam-dev/kubevela/version.GitRevision=git-$(git rev-parse --short HEAD)" \
    -o "${CTX}/manager" ./cmd/core
  cp entrypoint.sh "${CTX}/"
  docker build -q -t "${IMAGE}" -f hack/interview/Dockerfile "${CTX}"
fi

step "Importing ${IMAGE} into k3d-${CLUSTER}"
import_image "${IMAGE}"

step "Preloading the chart's webhook cert job image"
pull_and_import oamdev/kube-webhook-certgen:v2.4.1

step "Removing the local-development webhook configuration"
make webhook-clean

step "Upgrading vela-core to run in the cluster"
helm upgrade --install kubevela ./charts/vela-core \
  --namespace vela-system \
  --set replicaCount=1 \
  --set image.repository=vela-core \
  --set image.tag="${TAG}" \
  --set image.pullPolicy=Never \
  --set admissionWebhooks.enabled=true \
  --set multicluster.enabled=false \
  --wait --timeout 5m

step "Done"
echo "Logs: kubectl -n vela-system logs deploy/kubevela-vela-core -f"
