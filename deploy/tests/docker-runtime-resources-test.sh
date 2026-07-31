#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

fail() {
  printf 'docker runtime resources test failed: %s\n' "$1" >&2
  exit 1
}

assert_line() {
  file=$1
  line=$2
  grep -Fqx "$line" "$file" || fail "$file is missing: $line"
}

assert_docker_resources() {
  file=$1
  awk '
    /^dockers:/ { in_dockers = 1; next }
    in_dockers && /^[^[:space:]]/ { in_dockers = 0 }
    in_dockers && /^  - id:/ {
      if (seen && !has_resources) exit 1
      seen = 1
      has_resources = 0
      next
    }
    in_dockers && $0 == "      - backend/resources" { has_resources = 1 }
    END { if (!seen || !has_resources) exit 1 }
  ' "$file" || fail "$file has a Docker build without backend/resources"
}

test -s backend/resources/model-pricing/model_prices_and_context_window.json || \
  fail 'fallback pricing data is missing or empty'

assert_line Dockerfile.goreleaser 'COPY --chown=sub2api:sub2api backend/resources /app/resources'
assert_line deploy/Dockerfile 'COPY --from=backend-builder --chown=sub2api:sub2api /app/backend/resources /app/resources'
assert_docker_resources .goreleaser.yaml
test ! -e .goreleaser.simple.yaml || fail '.goreleaser.simple.yaml must remain removed in the fork'

printf 'docker runtime resources test passed\n'
