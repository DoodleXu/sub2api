#!/bin/sh

set -eu

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <tag> <default-branch-ref>" >&2
  exit 2
fi

tag_name=$1
default_branch_ref=$2

if ! printf '%s' "$tag_name" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'; then
  echo "invalid release tag: $tag_name" >&2
  exit 1
fi

tag_ref="refs/tags/$tag_name"
if ! git show-ref --verify --quiet "$tag_ref"; then
  echo "release tag does not exist: $tag_name" >&2
  exit 1
fi

tag_type=$(git cat-file -t "$tag_ref")
if [ "$tag_type" != "tag" ]; then
  echo "release tag must be annotated: $tag_name" >&2
  exit 1
fi

target_sha=$(git rev-parse --verify "$tag_name^{commit}")
if ! git rev-parse --verify --quiet "$default_branch_ref^{commit}" >/dev/null; then
  echo "default branch ref does not exist: $default_branch_ref" >&2
  exit 1
fi
if ! git merge-base --is-ancestor "$target_sha" "$default_branch_ref"; then
  echo "release commit $target_sha is not contained in $default_branch_ref" >&2
  exit 1
fi

tag_version=${tag_name#v}
committed_version=$(git show "$target_sha:backend/cmd/server/VERSION" | tr -d '\r\n')
if [ "$committed_version" != "$tag_version" ]; then
  echo "release tag version $tag_version does not match committed VERSION $committed_version" >&2
  exit 1
fi

tag_body=$(git for-each-ref --format='%(contents:body)' "$tag_ref")
if [ -z "$(printf '%s' "$tag_body" | tr -d '[:space:]')" ]; then
  echo "annotated release tag must include non-empty release notes: $tag_name" >&2
  exit 1
fi

printf '%s\n' "$target_sha"
