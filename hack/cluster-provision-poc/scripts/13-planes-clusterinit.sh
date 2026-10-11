#!/usr/bin/env bash
# Apply the two clusterInit planes and the blueprint that 14-dispatch.sh points the spoke at:
#   foundation-policy: Kyverno (helm-flux) plus one Audit ClusterPolicy (k8s-objects, after Kyverno)
#   foundation-certs:  cert-manager (helm-flux, CRDs from the chart)
#   cpspoke1-platform: the ClusterBlueprint composing both planes at phase clusterInit
# Both charts come from public index repositories, so no registry identity is needed on the spoke.
# The content is static, so it is a plain YAML file rather than a python-built one like
# 09-bootstrap-defs.sh (that one has to parse a Flux manifest). Idempotent: server-side apply.
set -Eeuo pipefail
source "$(dirname "$0")/env.sh"
export KUBECONFIG=$HUB_KC
kubectl() { command kubectl --request-timeout=15s "$@"; }
FILE=$OUT/planes-clusterinit.yaml

cat > "$FILE" <<EOF
apiVersion: core.oam.dev/v1beta1
kind: ClusterPlane
metadata:
  name: foundation-policy
  namespace: $NS
spec:
  description: Kyverno and a baseline Audit policy on the spoke
  components:
  - name: kyverno
    type: helm-flux
    properties:
      url: https://kyverno.github.io/kyverno/
      repoType: helm
      chart: kyverno
      version: "3.9.1"
      releaseName: kyverno
      targetNamespace: kyverno
      installTimeout: 10m
      values:
        admissionController:
          replicas: 1
        backgroundController:
          replicas: 1
        cleanupController:
          replicas: 1
        reportsController:
          replicas: 1
  - name: baseline-policies
    type: k8s-objects
    dependsOn: [kyverno]
    properties:
      objects:
      - apiVersion: kyverno.io/v1
        kind: ClusterPolicy
        metadata:
          name: require-app-label
        spec:
          background: true
          rules:
          - name: require-app-kubernetes-io-name
            match:
              any:
              - resources:
                  kinds: [Deployment]
            exclude:
              any:
              - resources:
                  namespaces: [kube-system, vela-system, flux-system, kyverno, cert-manager]
            validate:
              failureAction: Audit
              message: Deployments must carry app.kubernetes.io/name
              pattern:
                metadata:
                  labels:
                    app.kubernetes.io/name: "?*"
---
apiVersion: core.oam.dev/v1beta1
kind: ClusterPlane
metadata:
  name: foundation-certs
  namespace: $NS
spec:
  description: cert-manager on the spoke
  components:
  - name: cert-manager
    type: helm-flux
    properties:
      url: https://charts.jetstack.io
      repoType: helm
      chart: cert-manager
      version: v1.21.2
      releaseName: cert-manager
      targetNamespace: cert-manager
      installTimeout: 10m
      values:
        crds:
          enabled: true
---
apiVersion: core.oam.dev/v1beta1
kind: ClusterBlueprint
metadata:
  name: cpspoke1-platform
  namespace: $NS
spec:
  description: clusterInit planes for $SPOKE, policy and certificates
  planes:
  - name: foundation-policy
    ref:
      name: foundation-policy
    phase: clusterInit
  - name: foundation-certs
    ref:
      name: foundation-certs
    phase: clusterInit
EOF
echo "$FILE: 2 ClusterPlanes, 1 ClusterBlueprint"

kubectl apply --server-side --force-conflicts --field-manager=cluster-provision-poc -f "$FILE"   # the generator owns these; 15-dispatch-proof.sh patches the blueprint with another manager
kubectl get clusterplanes.core.oam.dev,clusterblueprints.core.oam.dev -n $NS
echo "REGISTER: ClusterPlanes foundation-policy and foundation-certs, ClusterBlueprint cpspoke1-platform in $NS on $HUB"
