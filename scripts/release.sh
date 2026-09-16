#!/bin/bash
# release.sh — merge a branch into main, push to GitHub, and tag a release.
#
# Usage: scripts/release.sh <tag> [branch]
#   <tag>    e.g. v0.15.3-alpha2 (must match the alpha < beta < rc < release
#            convention in internal/version; must not exist yet)
#   [branch] branch to merge (default: current branch)
#
# Pushes via the `github` remote, automatically falling back to the SSH
# URL when HTTPS is blocked (e.g. behind a proxy that can't reach
# github.com — SSH often still works). Pushing the tag is what triggers
# .github/workflows/release.yml (multi-arch images + binaries + release).
#
# Next step after this: scripts/monitor-release.sh <tag>
set -eu
cd "$(dirname "$0")/.."

TAG="${1:?usage: scripts/release.sh <tag> [branch]}"
BRANCH="${2:-$(git branch --show-current)}"

if [[ ! "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc)[0-9]+)?$ ]]; then
  echo "error: tag $TAG doesn't match vX.Y.Z[-alphaN|-betaN|-rcN]" >&2
  exit 1
fi
if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
  echo "error: tracked tree is dirty — commit or stash first" >&2
  git status --short | head -n 10
  exit 1
fi
git rev-parse --verify --quiet "$BRANCH" >/dev/null || { echo "error: no branch $BRANCH" >&2; exit 1; }

# Resolve a pushable URL for the github remote: prefer it as configured,
# fall back to SSH (derived from the configured URL) when HTTPS is blocked.
PUSH_URL="$(git remote get-url github)"
if ! git ls-remote "$PUSH_URL" HEAD >/dev/null 2>&1; then
  echo "note: $PUSH_URL unreachable, falling back to SSH"
  PUSH_URL="$(echo "$PUSH_URL" | sed -E 's#https://([^/]+)/#git@\1:#')"
  git ls-remote "$PUSH_URL" HEAD >/dev/null || { echo "error: SSH fallback also unreachable: $PUSH_URL" >&2; exit 1; }
fi
if git rev-parse --verify --quiet "refs/tags/$TAG" >/dev/null; then
  echo "error: local tag $TAG already exists" >&2
  exit 1
fi
if git ls-remote "$PUSH_URL" "refs/tags/$TAG" | grep -q .; then
  echo "error: tag $TAG already exists on the remote" >&2
  exit 1
fi

git checkout -q main
# Catch up with the remote first (fast-forward only — a divergence means
# someone else pushed; rebase/reconcile manually instead of forcing).
git fetch -q "$PUSH_URL" main
git merge -q --ff-only FETCH_HEAD
git merge --no-ff "$BRANCH" -m "Merge branch '$BRANCH'"

git push "$PUSH_URL" main
git push "$PUSH_URL" "$BRANCH"
git tag -a "$TAG" -m "$TAG"
git push "$PUSH_URL" "$TAG"

echo "released $TAG — watch it with: scripts/monitor-release.sh $TAG"
