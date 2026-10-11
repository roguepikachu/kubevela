#!/usr/bin/env bash
# Package the worktree's vela-core chart and push it to ECR as an OCI chart so the
# foundation-runtime plane's vela component (type helm-flux) can pull it from inside the
# spoke. Chart version is 0.1.0-cp.<TAG>, TAG being the worktree's short HEAD unless
# CHART_TAG is set (used to publish a second version for the 11 step 2 proof).
# Idempotent: exits 0 when that chart version is already in ECR. Helm 4.x (registry login, push).
set -euo pipefail
source "$(dirname "$0")/env.sh"
CHART_REPO=$PREFIX/vela-core                      # helm push appends the chart name to oci://$ECR/$PREFIX
TAG=${CHART_TAG:-$(git -C $WORKTREE rev-parse --short HEAD)}
VERSION=0.1.0-cp.$TAG

aws ecr describe-repositories --repository-names $CHART_REPO --region $REGION >/dev/null 2>&1 || {
  aws ecr create-repository --repository-name $CHART_REPO --region $REGION --image-scanning-configuration scanOnPush=true \
    --tags $TAGS --query repository.repositoryUri --output text
  echo "REGISTER: ECR repo $CHART_REPO"
}

if aws ecr describe-images --repository-name $CHART_REPO --region $REGION --image-ids imageTag=$VERSION >/dev/null 2>&1; then
  echo "chart $CHART_REPO:$VERSION already pushed"; exit 0
fi

helm package $WORKTREE/charts/vela-core --version $VERSION --app-version $TAG -d $OUT
aws ecr get-login-password --region $REGION | helm registry login $ECR -u AWS --password-stdin
helm push $OUT/vela-core-$VERSION.tgz oci://$ECR/$PREFIX

aws ecr describe-images --repository-name $CHART_REPO --region $REGION --image-ids imageTag=$VERSION \
  --query 'imageDetails[0].[imageTags[0],artifactMediaType,imagePushedAt]' --output text
echo "REGISTER: chart $CHART_REPO:$VERSION"
