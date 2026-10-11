#!/usr/bin/env bash
# Apply the helm-flux ComponentDefinition, then the foundation-runtime ClusterPlane: the namespaces
# Flux and vela-core live in, the Flux helm runtime (source-controller and helm-controller) as a
# ref-objects component the hub fetches from the fork at render time, and vela-core as a helm-flux
# release that pulls the chart 08-chart.sh pushed to ECR. 10-bootstrap.sh points the SpokeCluster
# at this plane and the hub pushes all three onto the spoke. Chart version is 0.1.0-cp.<TAG>, TAG
# being the worktree's short HEAD unless CHART_TAG is set (same rule as 08-chart.sh). Idempotent:
# def apply and kubectl apply update.
set -Eeuo pipefail
source "$(dirname "$0")/env.sh"
export KUBECONFIG=$HUB_KC
TAG=${CHART_TAG:-$(git -C $WORKTREE rev-parse --short HEAD)}
DEF=$WORKTREE/vela-templates/definitions/cluster-infra/component/helm-flux.cue
# The Flux manifest lives in the fork at vela-templates/definitions/cluster-infra/manifests/. ref-objects
# urls are fetched on the hub while the Application renders (pkg/appfile/parser.go, HTTPGetKubernetesObjects),
# so the branch has to be pushed and the hub needs egress to raw.githubusercontent.com.
FLUX_URL=${FLUX_URL:-https://raw.githubusercontent.com/roguepikachu/kubevela/poc/cluster-provision/vela-templates/definitions/cluster-infra/manifests/flux-v2.9.6-source-helm.yaml}
PLANE=$OUT/plane-foundation-runtime.yaml

/tmp/vela def apply $DEF -n $NS
curl -fsS -o /dev/null --max-time 15 "$FLUX_URL" || echo "WARNING: $FLUX_URL is not fetchable from here; the hub cannot render the flux component until the branch is pushed"

# Namespaces go in their own first component: vela-core dry-runs every object of a component on the
# spoke before creating any of them, so a Namespace inside the same component as its tenants does
# not satisfy the dry run ("namespaces flux-system not found", seen 2026-10-11). The HelmRepository
# and HelmRelease of the vela-core component land in vela-system, which the chart's createNamespace
# does not cover either, so that namespace is created here too. flux-system carries the labels of
# the upstream manifest, which the flux component applies again, so both components agree on it.
cat > "$PLANE" <<EOF
apiVersion: core.oam.dev/v1beta1
kind: ClusterPlane
metadata:
  name: foundation-runtime
  namespace: $NS
spec:
  description: Flux helm runtime and vela-core on the spoke, pushed by the hub
  components:
  - name: namespaces
    type: k8s-objects
    properties:
      objects:
      - apiVersion: v1
        kind: Namespace
        metadata:
          name: flux-system
          labels:
            app.kubernetes.io/instance: flux-system
            app.kubernetes.io/part-of: flux
            app.kubernetes.io/version: v2.9.6
            pod-security.kubernetes.io/warn: restricted
            pod-security.kubernetes.io/warn-version: latest
      - apiVersion: v1
        kind: Namespace
        metadata:
          name: vela-system
  - name: flux
    type: ref-objects
    dependsOn: [namespaces]
    properties:
      urls:
      - $FLUX_URL
  - name: vela-core                     # helm-flux names the HelmRelease after the component
    type: helm-flux
    dependsOn: [flux]
    properties:
      url: oci://$ECR/$PREFIX
      repoType: oci
      provider: aws
      chart: vela-core
      version: 0.1.0-cp.$TAG
      releaseName: vela-core
      targetNamespace: vela-system
      installTimeout: 10m
      values:
        image: {repository: $ECR/$REPO, tag: $TAG, pullPolicy: IfNotPresent}
        featureGates: {enableClusterInfrastructure: true}
        multicluster: {enabled: true}   # vela-cluster-core blocks at startup until the cluster-gateway APIService exists (seen 2026-10-11), so a leaf spoke still needs the gateway
        clusterCore: {replicaCount: 1}
EOF

kubectl apply --server-side --force-conflicts --field-manager=cluster-provision-poc -f "$PLANE"   # the generator owns the plane; proof scripts patch it with other managers
kubectl get componentdefinition helm-flux -n $NS -o jsonpath='{.metadata.name}{"\n"}'
kubectl get clusterplanes.core.oam.dev -n $NS
echo "== $PLANE ($(wc -l < "$PLANE") lines):"
cat "$PLANE"
