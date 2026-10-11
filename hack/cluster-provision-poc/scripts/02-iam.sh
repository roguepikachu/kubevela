#!/usr/bin/env bash
# Four roles under the tenant-gokarna-cp- prefix plus one inline policy on the Connect hub role.
# CAPA controller: Pod Identity trust, permissions from clusterawsadm print-policy rescoped to
# our prefix and split into two managed policies (6144-character limit). Control plane and
# node roles: AWS managed policies. Spoke role: trusted by the hub role with the external ID.
set -euo pipefail
source "$(dirname "$0")/env.sh"
cd "$OUT"
CLUSTERAWSADM=$BIN/clusterawsadm
[ -x $CLUSTERAWSADM ] || { curl -fsSL -o $CLUSTERAWSADM https://github.com/kubernetes-sigs/cluster-api-provider-aws/releases/download/$CAPA_VERSION/clusterawsadm-linux-$ARCH && chmod +x $CLUSTERAWSADM; }

ensure_role() { # name trust-file description
  if aws iam get-role --role-name $1 >/dev/null 2>&1; then
    aws iam update-assume-role-policy --role-name $1 --policy-document file://$2
  else
    aws iam create-role --role-name $1 --assume-role-policy-document file://$2 --description "$3" --tags $TAGS --query Role.Arn --output text
  fi
}

# 1. CAPA controller role
cat > capa-trust.json <<EOF
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"pods.eks.amazonaws.com"},
 "Action":["sts:AssumeRole","sts:TagSession"],
 "Condition":{"StringEquals":{"aws:RequestTag/eks-cluster-name":"${HUB}","aws:RequestTag/kubernetes-namespace":"${CAPA_NS}","aws:RequestTag/kubernetes-service-account":"${CAPA_SA}"}}}]}
EOF
ensure_role $CAPA_ROLE capa-trust.json "Provisioning POC: CAPA controller on chub1 via Pod Identity"
{ $CLUSTERAWSADM bootstrap iam print-policy --document AWSIAMManagedPolicyControllers; $CLUSTERAWSADM bootstrap iam print-policy --document AWSIAMManagedPolicyControllersEKS; } \
  | jq -s '{Version: .[0].Properties.PolicyDocument.Version, Statement: (map(.Properties.PolicyDocument.Statement) | add)}' \
  | jq 'walk(if type=="string" then gsub("\\*\\.cluster-api-provider-aws\\.sigs\\.k8s\\.io"; "'"${PREFIX}"'-*") else . end)' > capa-policy-full.json
n=$(jq '.Statement|length' capa-policy-full.json)
parts=2
while :; do
  per=$(( (n + parts - 1) / parts )); toobig=0
  for i in $(seq 1 $parts); do
    s=$(( (i-1)*per )); jq --argjson s $s --argjson e $(( s+per )) '{Version, Statement: .Statement[$s:$e]}' capa-policy-full.json > capa-policy-$i.json
    [ "$(jq -c . capa-policy-$i.json | wc -c)" -gt 6000 ] && toobig=1
  done
  [ $toobig = 0 ] && break; parts=$((parts+1)); [ $parts -le 6 ] || { echo "policy will not fit in 6 parts"; exit 1; }
done
echo "CAPA controller policy: $n statements in $parts managed policies"
for i in $(seq 1 $parts); do
  arn=arn:aws:iam::${ACCT}:policy/${CAPA_ROLE}-$i
  if aws iam get-policy --policy-arn $arn >/dev/null 2>&1; then
    aws iam create-policy-version --policy-arn $arn --policy-document file://capa-policy-$i.json --set-as-default --query PolicyVersion.VersionId --output text
    aws iam list-policy-versions --policy-arn $arn --query 'Versions[?!IsDefaultVersion].VersionId' --output text | tr '\t' '\n' | while read -r old; do [ -n "$old" ] && aws iam delete-policy-version --policy-arn $arn --version-id $old; done
  else
    aws iam create-policy --policy-name ${CAPA_ROLE}-$i --policy-document file://capa-policy-$i.json --tags $TAGS --query Policy.Arn --output text
  fi
  aws iam attach-role-policy --role-name $CAPA_ROLE --policy-arn $arn
done
for i in $(seq $((parts+1)) 6); do arn=arn:aws:iam::${ACCT}:policy/${CAPA_ROLE}-$i; aws iam detach-role-policy --role-name $CAPA_ROLE --policy-arn $arn 2>/dev/null && aws iam delete-policy --policy-arn $arn 2>/dev/null || true; done

# 2. EKS control plane and node roles (shared by every substrate)
echo '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"eks.amazonaws.com"},"Action":"sts:AssumeRole"}]}' > cp-trust.json
ensure_role $CP_ROLE cp-trust.json "Provisioning POC: EKS control plane role"
aws iam attach-role-policy --role-name $CP_ROLE --policy-arn arn:aws:iam::aws:policy/AmazonEKSClusterPolicy
echo '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}' > node-trust.json
ensure_role $NODE_ROLE node-trust.json "Provisioning POC: EKS managed node group role"
for p in AmazonEKSWorkerNodePolicy AmazonEKS_CNI_Policy AmazonEC2ContainerRegistryReadOnly; do
  aws iam attach-role-policy --role-name $NODE_ROLE --policy-arn arn:aws:iam::aws:policy/$p
done

# 3. Spoke role for the cluster CAPA will create, trusted by the Connect hub role
cat > spoke-trust.json <<EOF
{"Version":"2012-10-17","Statement":[
 {"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::${ACCT}:role/${HUB_ROLE}"},"Action":"sts:AssumeRole","Condition":{"StringEquals":{"sts:ExternalId":"${EXTERNAL_ID}"}}},
 {"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::${ACCT}:role/${HUB_ROLE}"},"Action":"sts:TagSession"}]}
EOF
ensure_role $SPOKE_ROLE spoke-trust.json "Provisioning POC: spoke role for $EKS_NAME"
cat > spoke-perms.json <<EOF
{"Version":"2012-10-17","Statement":[{"Sid":"DescribeOwnCluster","Effect":"Allow","Action":"eks:DescribeCluster","Resource":"arn:aws:eks:${REGION}:${ACCT}:cluster/${EKS_NAME}"}]}
EOF
aws iam put-role-policy --role-name $SPOKE_ROLE --policy-name describe-own-cluster --policy-document file://spoke-perms.json

# 4. Let the hub role assume every cp spoke role (MODIFIES the pre-existing Connect hub role)
cat > hub-cp-perms.json <<EOF
{"Version":"2012-10-17","Statement":[{"Sid":"AssumeCpSpokeRoles","Effect":"Allow","Action":["sts:AssumeRole","sts:TagSession"],"Resource":"arn:aws:iam::${ACCT}:role/${PREFIX}-spoke-*"}]}
EOF
aws iam put-role-policy --role-name $HUB_ROLE --policy-name assume-cp-spoke-roles --policy-document file://hub-cp-perms.json

echo "== simulator: hub -> spoke role (expect allowed), capa -> iam:PassRole to the cluster roles (expect allowed)"
aws iam simulate-principal-policy --policy-source-arn arn:aws:iam::${ACCT}:role/$HUB_ROLE --action-names sts:AssumeRole \
  --resource-arns arn:aws:iam::${ACCT}:role/$SPOKE_ROLE --output json | jq -r '.EvaluationResults[]?.ResourceSpecificResults[]? | [.EvalResourceName, .EvalResourceDecision] | @tsv'
aws iam simulate-principal-policy --policy-source-arn arn:aws:iam::${ACCT}:role/$CAPA_ROLE --action-names iam:PassRole \
  --resource-arns arn:aws:iam::${ACCT}:role/$CP_ROLE arn:aws:iam::${ACCT}:role/$NODE_ROLE \
  --context-entries ContextKeyName=iam:PassedToService,ContextKeyValues=eks.amazonaws.com,ContextKeyType=string \
  --output json | jq -r '.EvaluationResults[]? | [.EvalActionName, .EvalDecision] | @tsv'
echo "REGISTER: roles $CAPA_ROLE $CP_ROLE $NODE_ROLE $SPOKE_ROLE, managed policies ${CAPA_ROLE}-1..${CAPA_ROLE}-$parts, hub role inline policy assume-cp-spoke-roles (modification)"
