#!/usr/bin/env bash
# End-to-end test against the real Go module proxy, GitHub API and Docker Hub,
# using testdata/sample, a small project with deliberately old dependencies.
set -euo pipefail

bin=${BIN:-$PWD/bin/depbot}
fail() { echo "e2e: $*" >&2; exit 1; }

# 1. Every ecosystem reports an update, and --exit-code signals it.
set +e
"$bin" check --exit-code --output json testdata/sample > updates.json
code=$?
set -e
[ "$code" -eq 1 ] || fail "outdated sample must exit 1, got $code"
for eco in gomod github-actions docker; do
  jq -e --arg e "$eco" 'any(.[]; .ecosystem == $e)' updates.json >/dev/null || fail "no $eco update found"
done
"$bin" check --output markdown testdata/sample | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"

# 2. Applying the updates keeps the program building, and nothing is left to update.
work=$(mktemp -d)
cp -R testdata/sample/. "$work"
"$bin" update "$work"
(cd "$work" && go build ./...) || fail "updated sample does not build"
"$bin" check --exit-code "$work" || fail "updates remain after depbot update"

# 3. --pin rewrites action tags to commit SHAs with the version in a comment.
"$bin" update --ecosystem github-actions --pin "$work"
grep -Eq 'uses: actions/checkout@[0-9a-f]{40} # v[0-9]+' "$work/.github/workflows/ci.yml" || fail "actions/checkout was not pinned"

# 4. A pull request dry run plans one PR per dependency without writing anything.
if [ -n "${GITHUB_REPOSITORY:-}" ]; then
  "$bin" pr --dry-run --repo "$GITHUB_REPOSITORY" testdata/sample | tee pr.log
  [ "$(grep -c '^would open' pr.log)" -ge 3 ] || fail "expected pull requests for each dependency"
fi
echo "e2e: all expectations met"
