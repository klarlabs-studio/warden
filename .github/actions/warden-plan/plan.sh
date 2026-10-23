#!/usr/bin/env bash
set -euo pipefail
valid=false
reusable='[]'
run='[]'
sha=''
ms=''
report=''
eligible='[]'
if report=$(bash "$GITHUB_ACTION_PATH/../ci-plan.sh"); then
  valid=true
  eligible=$(jq -c .eligible_jobs <<<"$report")
  reusable=$(jq -c '.reusable_jobs' <<<"$report")
  run=$(jq -c '.run_jobs' <<<"$report")
  sha=$(jq -r '.sha' <<<"$report")
  ms=$(jq -r '.verification_ms' <<<"$report")
else
  report=''
fi
{
  echo "eligible-jobs=$eligible"
  echo "plan-json=$report"
  echo "plan-valid=$valid"
  echo "reusable-jobs=$reusable"
  echo "run-jobs=$run"
  echo "commit=$sha"
  echo "verification-ms=$ms"
} >> "$GITHUB_OUTPUT"
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  if [[ "$valid" == true ]]; then
    {
      echo '### Warden CI plan'
      echo
      echo "Mode: $(jq -r .mode <<<"$report"). Verification: ${ms}ms. Jobs matching contracts: $(jq '.eligible_jobs | length' <<<"$report"). Jobs requiring CI: $(jq '.run_jobs | length' <<<"$report")."
      echo
      echo 'Eligibility is not an observed skip or measured CI savings.'
    } >> "$GITHUB_STEP_SUMMARY"
  else
    echo 'Warden planning unavailable: no job is eligible for reuse; run CI checks.' >> "$GITHUB_STEP_SUMMARY"
  fi
fi
