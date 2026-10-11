# Shared settings for the provisioning POC. Sourced by every script here.
source ~/.dev/aws-envs/scratch2.env     # chub1 (scratch2) Atmos session creds; the user refreshes this file, not ~/.dev/aws.env
unset AWS_PROFILE                       # an empty AWS_PROFILE breaks aws and kubectl exec auth here

ACCT=776719623202
REGION=us-west-2
HUB=atmos-scratch-chub1
NS=vela-system
SA=vela-core-cluster-core
HUB_ROLE=tenant-gokarna-cc-hub           # the Connect hub role; gets one more inline policy
EXTERNAL_ID="${REGION}/${ACCT}/${HUB}/${NS}/${SA}"   # always ${ACCT}: zsh mangles $ACCT:role

PREFIX=tenant-gokarna-cp
ECR=${ACCT}.dkr.ecr.${REGION}.amazonaws.com
REPO=${PREFIX}-vela
CAPA_ROLE=${PREFIX}-capa-controller
CP_ROLE=${PREFIX}-eks-controlplane
NODE_ROLE=${PREFIX}-eks-nodes
SPOKE=cpspoke1
EKS_NAME=${PREFIX}-${SPOKE}
SPOKE_ROLE=${PREFIX}-spoke-${SPOKE}
VPC=vpc-0139f39b74825bd94                 # chub1 VPC after the 2026-10-10 rebuild (was vpc-0fc11f793558510be)
SUBNETS="subnet-0852d76a030ee2725 subnet-031dbec75effd936c subnet-0d79f7486601bb32e"   # chub1 private-subnet-{0,1,2} (2a,2b,2c) after the rebuild
K8S_VERSION=1.35
TAGS="Key=purpose,Value=cluster-provision-poc Key=owner,Value=aykumar"
TAG_MAP="purpose=cluster-provision-poc,owner=aykumar"

CAPI_VERSION=v1.14.3
CAPA_VERSION=v2.13.1
CAPA_NS=capa-system
CAPA_SA=capa-controller-manager

KV_CLONE=/workspaces/oam/opensource/kubevela
WORKTREE=/workspaces/oam/opensource/kubevela.worktrees/cluster-provision
POC=/workspaces/oam/cluster-provision-poc
OUT=$POC/out; mkdir -p "$OUT"
HUB_KC=/tmp/cc-chub1.kubeconfig
ARCH=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
BIN=/tmp/cp-bin; mkdir -p $BIN
