#!/usr/bin/env bash
# Produce the two coverage reports the CRAP analyzer reads.
#
#   scripts/coverage.sh          # both
#   scripts/coverage.sh go       # Go only
#   scripts/coverage.sh web      # frontend only
#
# Two things here are easy to get wrong and were both wrong once:
#
#   * This repo is a Go *workspace*. `go test ./...` from the root covers the root
#     module only -- every module in go.work has to be run on its own and the
#     profiles merged, or its packages score as if they had no tests at all.
#     host/hosttest is the one most easily missed: it is a separate module, and
#     leaving it out made 21 callables look untested that are not.
#
#   * The frontend "types" modules are not type-only. They carry real functions
#     (pulseOf, verdictOf), so excluding them from the vitest run understates
#     coverage and invents violations. Only the entry point is excluded.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

what="${1:-all}"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

go_coverage() {
  # Every module in the workspace, including the test harness module.
  local modules=(. host host/hosttest plugins/hello plugins/pagewatch plugins/tid plugins/bidrl)
  for m in "${modules[@]}"; do
    local name
    name="$(echo "$m" | tr '/.' '__')"
    ( cd "$m" && go test ./... -coverpkg=./... -coverprofile="$tmp/$name.out" >/dev/null )
  done
  # Merge on the block key, keeping the highest hit count: a block covered by one
  # module's tests is covered, even if another module's run never reached it.
  {
    echo "mode: set"
    for f in "$tmp"/*.out; do tail -n +2 "$f"; done |
      awk '{k=$1" "$2; if(!(k in m)||$3>m[k])m[k]=$3} END{for(k in m) print k, m[k]}'
  } > .crap-coverage.out
  echo "wrote .crap-coverage.out"
}

web_coverage() {
  ( cd web && npm run --silent test:coverage )
  cp web/coverage/cobertura-coverage.xml .crap-web-coverage.xml
  echo "wrote .crap-web-coverage.xml"
}

case "$what" in
  go)  go_coverage ;;
  web) web_coverage ;;
  all) go_coverage; web_coverage ;;
  *)   echo "usage: scripts/coverage.sh [go|web|all]" >&2; exit 2 ;;
esac
