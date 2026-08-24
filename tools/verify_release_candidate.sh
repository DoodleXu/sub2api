#!/bin/sh

# Verify the immutable release candidate before creating its annotated tag.
# This closes the gap where a valid tag existed but the exact commit had not
# passed the required CI and security workflows yet.
set -eu

if [ "$#" -ne 3 ]; then
  echo "usage: $0 <tag> <commit-ish> <owner/repo>" >&2
  exit 2
fi

tag_name=$1
target_ref=$2
repository=$3

if ! printf '%s' "$tag_name" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'; then
  echo "invalid release tag: $tag_name" >&2
  exit 1
fi

if ! command -v gh >/dev/null 2>&1; then
  echo "gh CLI is required to verify release candidate checks" >&2
  exit 1
fi

target_sha=$(git rev-parse --verify "$target_ref^{commit}")
tag_version=${tag_name#v}
committed_version=$(git show "$target_sha:backend/cmd/server/VERSION" | tr -d '\r\n')
if [ "$committed_version" != "$tag_version" ]; then
  echo "release tag version $tag_version does not match committed VERSION $committed_version" >&2
  exit 1
fi

default_branch=$(gh api "repos/$repository" --jq '.default_branch')
if [ -z "$default_branch" ]; then
  echo "Unable to resolve the default branch for $repository" >&2
  exit 1
fi

default_branch_sha=$(gh api --method GET \
  "repos/$repository/commits" \
  -f "sha=$default_branch" \
  -f 'per_page=1' \
  --jq '.[0].sha')
if [ -z "$default_branch_sha" ]; then
  echo "Unable to resolve $repository default branch head: $default_branch" >&2
  exit 1
fi

merge_base_sha=$(gh api \
  "repos/$repository/compare/$target_sha...$default_branch_sha" \
  --jq '.merge_base_commit.sha')
if [ "$merge_base_sha" != "$target_sha" ]; then
  echo "Release candidate $target_sha is not reachable from $repository default branch $default_branch ($default_branch_sha)" >&2
  exit 1
fi

for workflow in backend-ci.yml security-scan.yml; do
  successful_runs=$(gh api \
    "repos/$repository/actions/workflows/$workflow/runs?head_sha=$target_sha&status=completed&per_page=100" \
    --jq '[.workflow_runs[] | select(.conclusion == "success")] | length')
  if [ "$successful_runs" -lt 1 ]; then
    echo "No successful $workflow run found for $target_sha" >&2
    exit 1
  fi
done

printf '%s\n' "$target_sha"
