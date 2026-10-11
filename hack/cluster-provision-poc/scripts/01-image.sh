#!/usr/bin/env bash
# Build vela-core (manager + cluster-manager) for amd64 from the POC worktree and push to ECR
# without Docker: Go cross-compile, then crane append onto oamdev/vela-core:v1.10.9.
set -euo pipefail
source "$(dirname "$0")/env.sh"
TAG=$(git -C $WORKTREE rev-parse --short HEAD)

if aws ecr describe-images --repository-name $REPO --region $REGION --image-ids imageTag=$TAG >/dev/null 2>&1; then
  echo "$ECR/$REPO:$TAG already in ECR"; exit 0
fi
aws ecr describe-repositories --repository-names $REPO --region $REGION >/dev/null 2>&1 || \
  aws ecr create-repository --repository-name $REPO --region $REGION --image-scanning-configuration scanOnPush=true \
    --tags $TAGS --query repository.repositoryUri --output text

CRANE=$(command -v crane || echo ~/go/bin/crane)
B=$(mktemp -d); mkdir -p $B/usr/local/bin
LDF="-s -w -X github.com/oam-dev/kubevela/version.VelaVersion=cp-poc -X github.com/oam-dev/kubevela/version.GitRevision=$TAG"
(cd $WORKTREE && GOTOOLCHAIN=auto CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$LDF" -o $B/usr/local/bin/manager cmd/core/main.go)
(cd $WORKTREE && GOTOOLCHAIN=auto CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$LDF" -o $B/usr/local/bin/cluster-manager cmd/cluster-core/main.go)
cp $WORKTREE/entrypoint.sh $B/usr/local/bin/ && chmod 755 $B/usr/local/bin/*
tar --owner=0 --group=0 --numeric-owner -C $B -cf $B/layer.tar usr

aws ecr get-login-password --region $REGION | $CRANE auth login $ECR -u AWS --password-stdin
$CRANE append --platform linux/amd64 -b oamdev/vela-core:v1.10.9 -f $B/layer.tar -t $ECR/$REPO:$TAG
$CRANE config $ECR/$REPO:$TAG | jq -c '{arch: .architecture, ep: .config.Entrypoint}'
rm -rf $B
echo "REGISTER: ECR repo $REPO, tag $TAG"
