#!/usr/bin/env bash
#
# Deploy the backend to ECS without GitHub Actions.
#
# WHY THIS EXISTS
#   .github/workflows/deploy.yml is the normal path. When Actions cannot run at
#   all — a billing block stops jobs before any step executes — pushes produce no
#   deploy, and there is otherwise no way to ship. This reproduces what that
#   workflow does, with one deliberate difference (see TERRAFORM below).
#
# TERRAFORM
#   The workflow finishes with `terraform apply`, which needs ~20 TF_VAR_* secrets
#   that live only in GitHub Environments. This script does NOT run it: without
#   those values infra's unset_secrets_check precondition aborts the apply, which
#   is the guard that stops blank values overwriting real ones in Secrets Manager.
#
#   Instead it clones the CURRENT task definition and swaps only the image, so
#   every env var and secret reference carries over untouched. That means any
#   pending infra/ change (a new env var, a changed setting) is NOT applied — only
#   the code ships. Run the real workflow once Actions is healthy to pick those up.
#
#   Consequence: the revision this creates was made outside Terraform, so state
#   lags reality. It is self-healing — the script updates the SSM image parameter
#   the workflow reads, so the next `terraform apply` renders the same image and
#   converges instead of rolling the service back.
#
# USAGE
#   scripts/deploy-manual.sh staging
#   scripts/deploy-manual.sh production      # asks for confirmation
#
set -euo pipefail

ENVIRONMENT="${1:-}"
if [ "$ENVIRONMENT" != "staging" ] && [ "$ENVIRONMENT" != "production" ]; then
  echo "usage: $0 <staging|production>" >&2
  exit 2
fi

AWS_REGION="af-south-1"
ACCOUNT="659699822290"
REGISTRY="${ACCOUNT}.dkr.ecr.${AWS_REGION}.amazonaws.com"
ECR_REPO="fundis-${ENVIRONMENT}-backend"
CLUSTER="fundis-${ENVIRONMENT}-cluster"
SERVICE="fundis-${ENVIRONMENT}-backend"
SSM_IMAGE_PARAM="/fundis/${ENVIRONMENT}/backend/image"

cd "$(dirname "$0")/.."

# A dirty tree means the image would not match any commit, leaving nothing to roll
# back to or bisect from.
if [ -n "$(git status --porcelain)" ]; then
  echo "error: working tree is dirty; commit or stash first" >&2
  exit 1
fi
SHA="$(git rev-parse HEAD)"

if [ "$ENVIRONMENT" = "production" ]; then
  printf 'Deploy %s to PRODUCTION? [y/N] ' "${SHA:0:7}"
  read -r reply
  [ "$reply" = "y" ] || { echo "aborted"; exit 1; }
fi

# Either engine works; podman is the fallback when the user is not in the docker
# group. Both must produce linux/amd64 — Fargate tasks here declare no
# runtimePlatform, so they default to X86_64 regardless of the build host.
if docker info >/dev/null 2>&1; then
  ENGINE=docker
elif command -v podman >/dev/null 2>&1; then
  ENGINE=podman
else
  echo "error: neither docker (usable) nor podman found" >&2
  exit 1
fi
echo "==> engine: $ENGINE, commit: ${SHA:0:7}, target: $ENVIRONMENT"

echo "==> verifying before shipping"
go build ./...
go vet ./...
test -z "$(gofmt -l .)" || { echo "error: gofmt reports unformatted files" >&2; exit 1; }
go test -p 1 ./... >/dev/null

echo "==> building"
"$ENGINE" build --platform linux/amd64 -t "${REGISTRY}/${ECR_REPO}:${SHA}" .

echo "==> pushing"
aws ecr get-login-password --region "$AWS_REGION" \
  | "$ENGINE" login --username AWS --password-stdin "$REGISTRY"
"$ENGINE" push "${REGISTRY}/${ECR_REPO}:${SHA}"

# A digest rather than a tag keeps the reference immutable, matching the workflow.
DIGEST="$(aws ecr describe-images --repository-name "$ECR_REPO" \
  --image-ids "imageTag=${SHA}" --query 'imageDetails[0].imageDigest' \
  --output text --region "$AWS_REGION")"
IMAGE="${REGISTRY}/${ECR_REPO}@${DIGEST}"
echo "==> image: $IMAGE"

echo "==> publishing digest to SSM (so the next terraform apply converges)"
aws ssm put-parameter --name "$SSM_IMAGE_PARAM" --value "$IMAGE" \
  --type String --overwrite --region "$AWS_REGION" --query 'Version' --output text

CURRENT_TD="$(aws ecs describe-services --cluster "$CLUSTER" --services "$SERVICE" \
  --region "$AWS_REGION" --query 'services[0].taskDefinition' --output text)"
echo "==> current task definition: $CURRENT_TD  (rollback target)"

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT
aws ecs describe-task-definition --task-definition "$CURRENT_TD" --region "$AWS_REGION" \
  --query 'taskDefinition' --output json > "$WORKDIR/current.json"

IMAGE="$IMAGE" python3 - "$WORKDIR" <<'PY'
import json, os, sys

workdir = sys.argv[1]
td = json.load(open(f"{workdir}/current.json"))

# Fields describe an existing revision and are rejected by register-task-definition.
for key in ("taskDefinitionArn", "revision", "status", "requiresAttributes",
            "compatibilities", "registeredAt", "registeredBy", "deregisteredAt"):
    td.pop(key, None)

containers = td["containerDefinitions"]
if len(containers) != 1:
    sys.exit(f"expected exactly 1 container, found {len(containers)} — refusing to guess")

containers[0]["image"] = os.environ["IMAGE"]
json.dump(td, open(f"{workdir}/new.json", "w"))
print(f"    carried over: {len(containers[0].get('environment', []))} env vars, "
      f"{len(containers[0].get('secrets', []))} secret refs")
PY

NEW_TD="$(aws ecs register-task-definition --cli-input-json "file://$WORKDIR/new.json" \
  --region "$AWS_REGION" --query 'taskDefinition.taskDefinitionArn' --output text)"
echo "==> registered: $NEW_TD"

aws ecs update-service --cluster "$CLUSTER" --service "$SERVICE" \
  --task-definition "$NEW_TD" --region "$AWS_REGION" >/dev/null

echo "==> waiting for rollout"
for _ in $(seq 1 40); do
  read -r STATE COUNT <<<"$(aws ecs describe-services --cluster "$CLUSTER" --services "$SERVICE" \
    --region "$AWS_REGION" \
    --query 'services[0].[deployments[0].rolloutState,length(deployments)]' --output text)"
  echo "    $STATE (active deployments: $COUNT)"
  if [ "$STATE" = "COMPLETED" ] && [ "$COUNT" = "1" ]; then
    echo "==> deployed $NEW_TD"
    exit 0
  fi
  if [ "$STATE" = "FAILED" ]; then
    echo "error: rollout FAILED. Roll back with:" >&2
    echo "  aws ecs update-service --cluster $CLUSTER --service $SERVICE \\" >&2
    echo "    --task-definition $CURRENT_TD --region $AWS_REGION" >&2
    exit 1
  fi
  sleep 20
done

echo "error: rollout did not settle in time; check the service before retrying" >&2
echo "  rollback: aws ecs update-service --cluster $CLUSTER --service $SERVICE \\" >&2
echo "    --task-definition $CURRENT_TD --region $AWS_REGION" >&2
exit 1
