#!/usr/bin/env bash
# Controlled integration test: the temporary signer is never a production key.
set -euo pipefail
protected=$(realpath "$1")
source_repo=$(realpath "$2")
work=$(realpath "$3")
export PATH="$work/bin:$PATH"
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1 GIT_NO_REPLACE_OBJECTS=1
export WARDEN_CONFIG_DIR="$work/private-key"
artifacts="$work/observations"
mkdir -p "$artifacts"
template="$protected/.github/warden/linux-test/profiles/amd64/docker-policy-template.json"
git clone --bare --no-hardlinks "$source_repo" "$work/origin.git"
git --git-dir="$work/origin.git" update-ref refs/heads/main "$(git -C "$source_repo" rev-parse HEAD)"
git --git-dir="$work/origin.git" symbolic-ref HEAD refs/heads/main
git clone "$work/origin.git" "$work/producer"
cd "$work/producer"
git switch -C reuse-fixture
git config user.name warden-reuse-fixture
git config user.email warden-reuse-fixture
git config commit.gpgsign false
base=$(git rev-parse HEAD)
cp "$protected/.github/warden/linux-test/profiles/amd64/.warden.yaml" .warden.yaml
git add .warden.yaml
git commit --allow-empty -m 'test: select the reviewed container profile'
sha=$(git rev-parse HEAD)
ci-policy --repo "$PWD" --template "$template" --base "$base" --commit "$sha" \
  --branch reuse-fixture > "$artifacts/policy.json"
warden key show > "$artifacts/public-key.txt"
key=$(awk '/^fingerprint: / {print $2}' "$artifacts/public-key.txt")
[[ "$key" =~ ^[0-9a-f]{16}$ ]] || exit 1
export WARDEN_JOB_POLICY="$artifacts/policy.json" WARDEN_COMMIT="$sha"
export WARDEN_PLAN_MODE=enforce WARDEN_TRUSTED_KEYS="$key"
plan() { bash "$protected/.github/actions/ci-plan.sh"; }
assert_run() { jq -e '.run_jobs == ["test"] and .reusable_jobs == [] and .eligible_jobs == []' "$1" >/dev/null; }
plan > "$artifacts/before.json"
assert_run "$artifacts/before.json"
started=$(date +%s)
warden run pre-push --attest-only < /dev/null > "$artifacts/gate.log" 2>&1
elapsed=$(( $(date +%s) - started ))
# Assert the published artifact, not just the gate's exit status.
git --git-dir="$work/origin.git" notes --ref=warden show "$sha" > "$artifacts/note.json"
jq -e --arg sha "$sha" '.commit_sha == $sha and .contracts.test.version == 1 and
  .contracts.test.environment.platform == "linux/amd64" and
  .contracts.test.environment.network == "none" and .signature != null' "$artifacts/note.json" >/dev/null

# A fresh consumer gets the public note, not the signer's private directory.
git clone --no-hardlinks "$PWD" "$work/consumer"
cd "$work/consumer"
git config user.name warden-reuse-fixture
git config user.email warden-reuse-fixture
git remote set-url origin "$work/origin.git"
git fetch origin refs/notes/warden:refs/notes/warden
export WARDEN_CONFIG_DIR="$work/consumer-key"
plan > "$artifacts/after.json"
jq -e --arg sha "$sha" '.sha == $sha and .mode == "enforce" and
  .run_jobs == [] and .reusable_jobs == ["test"] and .eligible_jobs == ["test"]' \
  "$artifacts/after.json" >/dev/null

WARDEN_TRUSTED_KEYS=0000000000000000 plan > "$artifacts/untrusted.json"
assert_run "$artifacts/untrusted.json"
jq '.jobs.test.steps.test.platform = "linux/arm64"' "$artifacts/policy.json" > "$artifacts/wrong-platform-policy.json"
WARDEN_JOB_POLICY="$artifacts/wrong-platform-policy.json" plan > "$artifacts/wrong-platform.json"
assert_run "$artifacts/wrong-platform.json"
# A genuine signature transplanted to another commit must still fail closed.
git notes --ref=warden copy "$sha" "$base"
WARDEN_COMMIT="$base" plan > "$artifacts/replayed.json"
assert_run "$artifacts/replayed.json"
jq -n --arg sha "$sha" --arg base "$base" --argjson seconds "$elapsed" \
  '{version:1, experiment:"native CI fixture with temporary test signer", sha:$sha, base:$base,
    gate_seconds:$seconds, signed_note_published:true, protected_plan_reusable:true,
    production_trust_changed:false, remote_job_skipped:false, measured_ci_savings_seconds:null}' \
  > "$artifacts/result.json"
