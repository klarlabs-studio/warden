#!/usr/bin/env bash
# Only explicitly pinned trust may authorize CI reuse. Never fall back to the
# candidate checkout's roster: that checkout is the input being evaluated.
set -euo pipefail

if [[ -z "${WARDEN_TRUSTED_KEYS//[[:space:],]/}" ]]; then
  echo "validated=false" >> "$GITHUB_OUTPUT"
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

if warden "${REQUIREMENTS[@]}" --quiet; then
  echo "validated=true" >> "$GITHUB_OUTPUT"
  echo 'warden: trusted commit provenance verified; reuse only equivalent checks.'
else
  echo "validated=false" >> "$GITHUB_OUTPUT"
  echo 'warden: trusted commit provenance not verified; CI must run its checks.'
fi
