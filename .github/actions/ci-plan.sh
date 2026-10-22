#!/usr/bin/env bash
# Shared planner boundary; GitLab embeds this exact script in its component.
set -euo pipefail
refuse() { echo "warden-plan: $*" >&2; exit 1; }
[[ -n "${WARDEN_TRUSTED_KEYS//[[:space:],]/}" ]] || refuse 'explicit trusted keys required'
command -v jq >/dev/null || refuse 'jq required'
command -v realpath >/dev/null || refuse 'realpath required'
[[ -f "$WARDEN_JOB_POLICY" ]] || refuse 'protected job policy not found'
root=$(realpath -- "$(git rev-parse --show-toplevel)")
policy=$(realpath -- "$WARDEN_JOB_POLICY")
case "$policy" in "$root"/*) refuse 'job policy resolves inside the candidate checkout' ;; esac
sha=$(git rev-parse --verify --end-of-options "${WARDEN_COMMIT}^{commit}")
[[ "$sha" =~ ^([0-9a-f]{40}|[0-9a-f]{64})$ ]] || refuse 'invalid resolved commit'
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
warden ci plan --commit "$sha" --policy "$policy" --key "$WARDEN_TRUSTED_KEYS" --json > "$tmp/report.json"
# A complete, consistent partition of the protected inventory is mandatory.
# An unknown release, partial output or contradictory lists authorize no reuse.
jq -e -s --arg sha "$sha" --slurpfile policy "$policy" '
  def duration: type == "number" and . >= 0 and floor == .;
  length == 1 and ($policy | length) == 1 and (.[0] |
  $policy[0] as $p |
  $p.version == 1 and ($p.jobs | type) == "object" and ($p.jobs | length) > 0 and
  .version == 1 and .sha == $sha and (.verification_ms | duration) and
  (.jobs | type) == "array" and
  all(.jobs[];
    (.name | type) == "string" and (.name | test("^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$")) and
    (.action == "run" or .action == "skip") and
    (.reasons | type) == "array" and (.reasons | length) > 0 and
    all(.reasons[]; type == "string") and (.verification_ms | duration) and
    (.name as $n | $p.jobs | has($n)) and
    (.action != "skip" or (.name as $n | $p.jobs[$n] != null))) and
  ([.jobs[].name] | sort) == ($p.jobs | keys) and
  .run_jobs == ([.jobs[] | select(.action == "run") | .name] | sort) and
  .reusable_jobs == ([.jobs[] | select(.action == "skip") | .name] | sort))
' "$tmp/report.json" >/dev/null || refuse 'invalid or inconsistent planner output'
jq -c . "$tmp/report.json"
