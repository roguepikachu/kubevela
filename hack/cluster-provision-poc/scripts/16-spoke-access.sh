#!/usr/bin/env bash
# Give the operator's own role direct kubectl access to the spoke EKS cluster. One-time, idempotent,
# AWS writes: one EKS access entry on $EKS_NAME plus the AmazonEKSClusterAdminPolicy association.
# Why: without it the operator role has no access entry on $EKS_NAME (checked 2026-10-11), so every
# spoke read has to go through the hub's cluster-gateway proxy, and the hub-down steps of the proofs
# cannot scale the gateway to 0. With it, 14-dispatch.sh and 15-dispatch-proof.sh read the spoke
# through SPOKE_KC and the full hub-down variant runs.
#
# Role ARN reconstruction: sts get-caller-identity returns an assumed-role ARN
# arn:aws:sts::<acct>:assumed-role/<role>/<session>, which drops the IAM path. SSO roles
# (AWSReservedSSO_*) live under aws-reserved/sso.amazonaws.com/<region>/; any other role has no
# path. The reconstructed ARN is only a guess, so it is checked against iam get-role and the
# value IAM returns is the one used. Access entries are keyed by the exact IAM ARN, so a wrong
# path would create an entry nobody can use.
set -Eeuo pipefail
source "$(dirname "$0")/env.sh"
SPOKE_KC=/tmp/cp-spoke.kubeconfig
ADMIN_POLICY=arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy

caller_arn=$(aws sts get-caller-identity --query Arn --output text)
echo "caller: $caller_arn"
case "$caller_arn" in
  *:assumed-role/*) role_name=$(echo "$caller_arn" | sed -E 's#.*:assumed-role/([^/]+)/.*#\1#');;
  *) echo "caller is not an assumed role; an access entry needs an IAM role or user ARN, got $caller_arn"; exit 1;;
esac
case "$role_name" in
  AWSReservedSSO_*) guess=arn:aws:iam::${ACCT}:role/aws-reserved/sso.amazonaws.com/${REGION}/$role_name;;
  *) guess=arn:aws:iam::${ACCT}:role/$role_name;;
esac
echo "reconstructed: $guess"
role_arn=$(aws iam get-role --role-name "$role_name" --query Role.Arn --output text)
[ -n "$role_arn" ] && [ "$role_arn" != None ] || { echo "iam get-role returned no ARN for $role_name"; exit 1; }
if [ "$role_arn" = "$guess" ]; then echo "verified: IAM agrees"; else echo "verified: IAM says $role_arn (path differs from the guess; using IAM's value)"; fi

if aws eks describe-access-entry --cluster-name $EKS_NAME --region $REGION --principal-arn "$role_arn" >/dev/null 2>&1; then
  echo "access entry for $role_arn exists on $EKS_NAME"
else
  aws eks create-access-entry --cluster-name $EKS_NAME --region $REGION --principal-arn "$role_arn" --type STANDARD --tags $TAG_MAP \
    --query 'accessEntry.accessEntryArn' --output text
  echo "REGISTER: EKS access entry $role_arn on $EKS_NAME (AmazonEKSClusterAdminPolicy)"
fi

if aws eks list-associated-access-policies --cluster-name $EKS_NAME --region $REGION --principal-arn "$role_arn" \
     --query 'associatedAccessPolicies[].policyArn' --output text | grep -q AmazonEKSClusterAdminPolicy; then
  echo "AmazonEKSClusterAdminPolicy already associated"
else
  aws eks associate-access-policy --cluster-name $EKS_NAME --region $REGION --principal-arn "$role_arn" \
    --policy-arn $ADMIN_POLICY --access-scope type=cluster --query 'associatedAccessPolicy.policyArn' --output text
  echo "associated AmazonEKSClusterAdminPolicy (cluster scope)"
fi

aws eks update-kubeconfig --name $EKS_NAME --region $REGION --kubeconfig $SPOKE_KC --alias $SPOKE
# A new access entry takes a little while to be honoured by the API server; 2 minutes is generous.
for i in $(seq 1 12); do
  if command kubectl --kubeconfig $SPOKE_KC --request-timeout=15s get ns >/dev/null 2>&1; then
    echo "ok: kubectl against $EKS_NAME works through $SPOKE_KC"
    command kubectl --kubeconfig $SPOKE_KC --request-timeout=15s get ns -o name | tr '\n' ' '; echo
    echo "export SPOKE_KC=$SPOKE_KC"
    exit 0
  fi
  echo "$(date -u +%H:%M:%S) spoke API not accepting the new entry yet (try $i/12)"; sleep 10
done
echo "kubectl through $SPOKE_KC still fails after 2 minutes; check the access entry with aws eks describe-access-entry"; exit 1
