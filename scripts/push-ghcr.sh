#!/usr/bin/env bash
# Build go-tilt-exporter for linux/amd64 and push to GHCR (private).
# Same flow as TriviaTV/scripts/push-ghcr.sh. Node-level GHCR auth exists on the
# cluster, so pods need no imagePullSecret.
#
# Usage:
#   ./scripts/push-ghcr.sh            # tag = short git SHA
#   TAG=v0.3.0 ./scripts/push-ghcr.sh
#
# Auth: GHCR_TOKEN env, or 1Password item "GitHub GHCR Token" (vault Home Lab).
# After the first push, set the package to private:
#   https://github.com/users/jtway/packages/container/go-tilt-exporter/settings
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

GHCR_USERNAME="${GHCR_USERNAME:-jtway}"
IMAGE_BASE="${IMAGE:-ghcr.io/${GHCR_USERNAME}/go-tilt-exporter}"
TAG="${TAG:-$(git rev-parse --short HEAD)}"
TOKEN="${GHCR_TOKEN:-$(op item get 'GitHub GHCR Token' --vault 'Home Lab' --fields label=credential --reveal 2>/dev/null || true)}"
[[ -n "$TOKEN" ]] || { echo "ERROR: set GHCR_TOKEN or sign in to op" >&2; exit 1; }
[[ -z "$(git status --porcelain --untracked-files=no)" ]] || echo "WARNING: uncommitted changes are included in ${TAG}" >&2

echo "$TOKEN" | docker login ghcr.io -u "$GHCR_USERNAME" --password-stdin
docker buildx inspect homelab-amd64 >/dev/null 2>&1 && docker buildx use homelab-amd64 \
  || docker buildx create --name homelab-amd64 --use >/dev/null

docker buildx build --platform linux/amd64 --build-arg VERSION="$TAG" \
  -t "${IMAGE_BASE}:${TAG}" --push .

DIGEST="$(docker buildx imagetools inspect "${IMAGE_BASE}:${TAG}" --format '{{json .Manifest.Digest}}' | tr -d '"')"
cat <<OUT

Pushed ${IMAGE_BASE}:${TAG}
In homelab-pulumi:
  pulumi config set brewtrackerImage ${IMAGE_BASE}:${TAG}@${DIGEST}
OUT
