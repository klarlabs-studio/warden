#!/usr/bin/env bash
# Only explicitly pinned trust may authorize CI reuse. Never fall back to the
# candidate checkout's roster: that checkout is the input being evaluated.
set -euo pipefail

# write_outputs VALIDATED SCOPE WORKSPACES_JSON
write_outputs() {
  {
    echo "validated=$1"
    echo "scope=$2"
    echo "validated-workspaces=$3"
  } >> "$GITHUB_OUTPUT"
}

if [[ -z "${WARDEN_TRUSTED_KEYS//[[:space:],]/}" ]]; then
  write_outputs false none '[]'
  echo '::notice::warden: no trusted key pinned in the action; CI must run its checks.'
  exit 0
fi

REQUIREMENTS=(verify --commit "$WARDEN_COMMIT" --key "$WARDEN_TRUSTED_KEYS")
if [[ -n "${WARDEN_REQUIREMENTS:-}" ]]; then
  REQUIREMENTS+=(--requirements "$WARDEN_REQUIREMENTS")
fi
if [[ -n "${WARDEN_CONTAINER_STEPS:-}" ]]; then
  REQUIREMENTS+=(--container-steps "$WARDEN_CONTAINER_STEPS")
fi
if [[ -n "${WARDEN_CONTAINER_IMAGE_ID:-}" ]]; then
  REQUIREMENTS+=(--container-image-id "$WARDEN_CONTAINER_IMAGE_ID")
fi
if [[ -n "${WARDEN_MIN_PROFILE:-}" ]]; then
  REQUIREMENTS+=(--min-profile "$WARDEN_MIN_PROFILE")
fi

# The exit code alone decides `validated`, exactly as before --json existed: a
# warden too old to print JSON for one commit prints text instead, with the
# same exit code. --json only adds the scope, read from warden's own JSON.
status=0
report="$(warden "${REQUIREMENTS[@]}" --json)" || status=$?
validated=false
if [[ $status -eq 0 ]]; then
  validated=true
fi

# The scope outputs speak only when the JSON agrees with the exit code: full
# for a pass, affected for a refusal of an affected-scope note. Anything else,
# including no jq, no JSON or a malformed field, is "none" with no workspaces,
# so a consumer that skips per-workspace work on them runs everything.
scope=none
workspaces='[]'
if command -v jq >/dev/null 2>&1; then
  claim="$(jq -r '
    if type == "object" and (.validated | type) == "boolean" and (.scope | type) == "string"
       and (.validated_workspaces | type) == "array" and all(.validated_workspaces[]; type == "string")
    then "\(.validated) \(.scope)" else empty end' <<<"$report" 2>/dev/null)" || claim=""
  if [[ ( $status -eq 0 && "$claim" == "true full" ) || ( $status -eq 1 && "$claim" == "false affected" ) ]]; then
    scope="${claim#* }"
    workspaces="$(jq -c '.validated_workspaces' <<<"$report")"
  fi
fi
if [[ $scope == none && $status -eq 0 ]]; then
  echo '::notice::warden: this warden release reports no scope for one commit (or jq is missing); scope=none, validated-workspaces=[].'
fi

write_outputs "$validated" "$scope" "$workspaces"
if [[ $validated == true ]]; then
  echo 'warden: trusted commit provenance verified; reuse only equivalent checks.'
elif [[ $scope == affected ]]; then
  echo "warden: trusted provenance of a partial (affected-scope) check; not validated. Workspaces checked on this tree: $workspaces"
else
  echo 'warden: trusted commit provenance not verified; CI must run its checks.'
fi
