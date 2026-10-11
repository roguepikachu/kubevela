#!/usr/bin/env bash
# Install CAPI core + CAPA on the hub with clusterctl, then switch the CAPA controller to Pod
# Identity. clusterctl detects Atmos's cert-manager (it creates a test Issuer and Certificate in
# namespace cert-manager-test) and skips installing its own. The aws provider manifest needs
# AWS_B64ENCODED_CREDENTIALS set; it is set to an empty profile and zeroed right after.
set -euo pipefail
source "$(dirname "$0")/env.sh"
CLUSTERCTL=$BIN/clusterctl; CLUSTERAWSADM=$BIN/clusterawsadm
[ -x $CLUSTERCTL ] || { curl -fsSL -o $CLUSTERCTL https://github.com/kubernetes-sigs/cluster-api/releases/download/$CAPI_VERSION/clusterctl-linux-$ARCH && chmod +x $CLUSTERCTL; }
[ -x $CLUSTERAWSADM ] || { curl -fsSL -o $CLUSTERAWSADM https://github.com/kubernetes-sigs/cluster-api-provider-aws/releases/download/$CAPA_VERSION/clusterawsadm-linux-$ARCH && chmod +x $CLUSTERAWSADM; }

if ! kubectl --kubeconfig $HUB_KC get deploy -n $CAPA_NS capa-controller-manager >/dev/null 2>&1; then
  export EXP_MACHINE_POOL=true
  export AWS_B64ENCODED_CREDENTIALS=$(printf '[default]\n' | base64 -w0)
  $CLUSTERCTL init --kubeconfig $HUB_KC --core cluster-api:$CAPI_VERSION --bootstrap "-" --control-plane "-" --infrastructure aws:$CAPA_VERSION
  unset AWS_B64ENCODED_CREDENTIALS
else
  echo "CAPA already installed"
fi
kubectl --kubeconfig $HUB_KC -n $CAPA_NS rollout status deploy/capa-controller-manager --timeout=5m

a=$(aws eks list-pod-identity-associations --cluster-name $HUB --region $REGION --namespace $CAPA_NS --service-account $CAPA_SA --query 'associations[0].associationId' --output text)
if [ -z "$a" ] || [ "$a" = None ]; then
  $CLUSTERAWSADM controller use-pod-identity --cluster-name $HUB --region $REGION --namespace $CAPA_NS --service-account $CAPA_SA --role-name $CAPA_ROLE
  aws eks list-pod-identity-associations --cluster-name $HUB --region $REGION --namespace $CAPA_NS --service-account $CAPA_SA --query 'associations[0].associationId' --output text \
    | xargs -I{} aws eks tag-resource --resource-arn arn:aws:eks:${REGION}:${ACCT}:podidentityassociation/${HUB}/{} --tags $TAG_MAP
  echo "waiting 30 s for the association to propagate"; sleep 30
else
  echo "association $a exists"
fi
$CLUSTERAWSADM controller zero-credentials --kubeconfig $HUB_KC --namespace $CAPA_NS
$CLUSTERAWSADM controller rollout-controller --kubeconfig $HUB_KC --namespace $CAPA_NS
kubectl --kubeconfig $HUB_KC -n $CAPA_NS rollout status deploy/capa-controller-manager --timeout=5m
P=$(kubectl --kubeconfig $HUB_KC get pod -n $CAPA_NS -l control-plane=capa-controller-manager --field-selector=status.phase=Running --sort-by=.metadata.creationTimestamp -o jsonpath='{.items[-1].metadata.name}')
kubectl --kubeconfig $HUB_KC get pod $P -n $CAPA_NS -o jsonpath='{.spec.containers[0].env[*].name}' | grep -q AWS_CONTAINER_CREDENTIALS_FULL_URI && echo "Pod Identity env present on $P" || { echo "MISSING Pod Identity env on $P"; exit 1; }
kubectl --kubeconfig $HUB_KC get deploy -n $CAPA_NS capa-controller-manager -o jsonpath='{.spec.template.spec.containers[0].args}' | tr ',' '\n' | grep -i feature || true
echo "REGISTER: CAPI/CAPA install on chub1 (namespaces capi-system, capa-system, cert-manager-test), Pod Identity association for $CAPA_NS/$CAPA_SA"
