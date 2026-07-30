#!/bin/sh

set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
verify_script="$script_dir/verify_release_tag.sh"
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/sub2api-release-tag.XXXXXX")
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM

repo_dir="$work_dir/repo"
mkdir -p "$repo_dir/backend/cmd/server"
git -C "$repo_dir" init -q
git -C "$repo_dir" config user.name "Release Test"
git -C "$repo_dir" config user.email "release-test@example.invalid"

printf '1.2.3\n' >"$repo_dir/backend/cmd/server/VERSION"
git -C "$repo_dir" add backend/cmd/server/VERSION
git -C "$repo_dir" commit -qm "chore: prepare v1.2.3"
git -C "$repo_dir" branch -M main
first_sha=$(git -C "$repo_dir" rev-parse HEAD)
git -C "$repo_dir" tag -a v1.2.3 -m "release v1.2.3" -m "Validated release notes."

run_verify() {
  (
    cd "$repo_dir"
    "$verify_script" "$@"
  )
}

expect_failure() {
  expected_message=$1
  shift

  if output=$(run_verify "$@" 2>&1); then
    echo "expected verification failure for $*" >&2
    exit 1
  fi
  case "$output" in
    *"$expected_message"*) ;;
    *)
      echo "unexpected verification error for $*: $output" >&2
      exit 1
      ;;
  esac
}

verified_sha=$(run_verify v1.2.3 main)
if [ "$verified_sha" != "$first_sha" ]; then
  echo "verified SHA $verified_sha does not match $first_sha" >&2
  exit 1
fi

expect_failure "invalid release tag" v1.2.3+build.1 main

git -C "$repo_dir" tag v1.2.4
expect_failure "must be annotated" v1.2.4 main

git -C "$repo_dir" tag -a v1.2.4-mismatch -m "release v1.2.4-mismatch" -m "Release notes."
expect_failure "does not match committed VERSION" v1.2.4-mismatch main

printf '1.2.4-empty\n' >"$repo_dir/backend/cmd/server/VERSION"
git -C "$repo_dir" add backend/cmd/server/VERSION
git -C "$repo_dir" commit -qm "chore: prepare empty-notes fixture"
git -C "$repo_dir" tag -a v1.2.4-empty -m "release v1.2.4-empty"
expect_failure "must include non-empty release notes" v1.2.4-empty main

git -C "$repo_dir" switch -qc side "$first_sha"
printf '1.2.5\n' >"$repo_dir/backend/cmd/server/VERSION"
git -C "$repo_dir" add backend/cmd/server/VERSION
git -C "$repo_dir" commit -qm "chore: prepare side release"
git -C "$repo_dir" tag -a v1.2.5 -m "release v1.2.5" -m "Side branch release notes."
git -C "$repo_dir" switch -q main
expect_failure "is not contained in main" v1.2.5 main

echo "Release tag verification tests passed."
