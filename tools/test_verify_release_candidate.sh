#!/bin/sh

set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
verify_script="$script_dir/verify_release_candidate.sh"
work_dir=$(mktemp -d "${TMPDIR:-/tmp}/sub2api-release-candidate.XXXXXX")
trap 'rm -rf "$work_dir"' EXIT HUP INT TERM

repo_dir="$work_dir/repo"
bin_dir="$work_dir/bin"
mkdir -p "$repo_dir/backend/cmd/server" "$bin_dir"
git -C "$repo_dir" init -q
git -C "$repo_dir" config user.name "Release Test"
git -C "$repo_dir" config user.email "release-test@example.invalid"
printf '1.2.3\n' >"$repo_dir/backend/cmd/server/VERSION"
git -C "$repo_dir" add backend/cmd/server/VERSION
git -C "$repo_dir" commit -qm "chore: 准备发布 v1.2.3"
target_sha=$(git -C "$repo_dir" rev-parse HEAD)

cat >"$bin_dir/gh" <<'EOF'
#!/bin/sh
set -eu
endpoint=''
for argument in "$@"; do
  case "$argument" in
    repos/*) endpoint=$argument ;;
  esac
done

case "$endpoint" in
  */actions/workflows/*)
    case "${GH_STUB_RESULT:-success}" in
      success) printf '1\n' ;;
      failure) printf '0\n' ;;
      *) exit 1 ;;
    esac
    ;;
  */commits)
    printf '%s\n' "${GH_STUB_DEFAULT_SHA:?missing GH_STUB_DEFAULT_SHA}"
    ;;
  */compare/*)
    printf '%s\n' "${GH_STUB_MERGE_BASE_SHA:?missing GH_STUB_MERGE_BASE_SHA}"
    ;;
  repos/*)
    printf '%s\n' main
    ;;
  *)
    echo "unexpected gh invocation: $*" >&2
    exit 1
    ;;
esac
EOF
chmod +x "$bin_dir/gh"

run_verify() {
  (
    cd "$repo_dir"
    PATH="$bin_dir:$PATH" /bin/sh "$verify_script" "$@"
  )
}

verified_sha=$(
  GH_STUB_DEFAULT_SHA=$target_sha \
  GH_STUB_MERGE_BASE_SHA=$target_sha \
  run_verify v1.2.3 HEAD DoodleXu/sub2api
)
if [ "$verified_sha" != "$target_sha" ]; then
  echo "verified SHA $verified_sha does not match $target_sha" >&2
  exit 1
fi

if GH_STUB_DEFAULT_SHA=$target_sha GH_STUB_MERGE_BASE_SHA=$target_sha GH_STUB_RESULT=failure \
  run_verify v1.2.3 HEAD DoodleXu/sub2api >/dev/null 2>&1; then
  echo "expected required-check verification failure" >&2
  exit 1
fi

if GH_STUB_DEFAULT_SHA=$target_sha GH_STUB_MERGE_BASE_SHA=$target_sha \
  run_verify v1.2.4 HEAD DoodleXu/sub2api >/dev/null 2>&1; then
  echo "expected VERSION mismatch failure" >&2
  exit 1
fi

git -C "$repo_dir" switch -qc side-branch
printf 'side branch\n' >"$repo_dir/side-branch.txt"
git -C "$repo_dir" add side-branch.txt
git -C "$repo_dir" commit -qm "test: create side branch candidate"
side_branch_sha=$(git -C "$repo_dir" rev-parse HEAD)

if GH_STUB_DEFAULT_SHA=$target_sha GH_STUB_MERGE_BASE_SHA=$target_sha \
  run_verify v1.2.3 "$side_branch_sha" DoodleXu/sub2api >/dev/null 2>&1; then
  echo "expected side-branch candidate rejection" >&2
  exit 1
fi

echo "Release candidate verification tests passed."
