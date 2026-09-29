#!/usr/bin/env bash
# Shared helpers for the interview scripts. Source, don't run.

step() { printf '\n==> %s\n' "$*"; }

# Architecture of the Docker engine, which is what the k3d nodes run on.
DOCKER_ARCH="$(docker version -f '{{.Server.Arch}}')"

# import_image <image> loads a local Docker image into every node of $CLUSTER.
#
# This replaces `k3d image import`: with Docker's containerd image store (the
# default in recent Docker Desktop and Docker Engine 29+), `docker save` of a
# pulled image writes a multi-platform index whose other-platform blobs are
# missing, and k3d's import then fails inside the node while still reporting
# success. Saving only the local platform avoids that.
import_image() {
  local img="$1" role node
  for role in server agent; do
    for node in $(docker ps --filter "label=k3d.cluster=${CLUSTER}" --filter "label=k3d.role=${role}" --format '{{.Names}}'); do
      docker save --platform "linux/${DOCKER_ARCH}" "${img}" | docker exec -i "${node}" ctr -n k8s.io images import - >/dev/null
    done
  done
  echo "imported ${img}"
}

# pull_and_import <image> pulls on the host, then loads into the cluster.
pull_and_import() {
  docker pull -q "$1" >/dev/null
  import_image "$1"
}
