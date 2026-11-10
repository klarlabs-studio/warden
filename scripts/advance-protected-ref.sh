#!/usr/bin/env bash
# Advance the protected test-reuse ref to a reviewed merge on main.
#
# Adopting a republished Linux test image takes two pull requests: the first
# changes the receipts, profiles and ci.yml contract; this script prepares the
# second, which points the protected verifier (ci.yml) and the native fixture
# (native-container-reuse.yml) at the first one's merge commit. Commit the
# result on a branch and open a pull request as usual.
set -euo pipefail

new=${1:-}
if [[ ! $new =~ ^[0-9a-f]{40}$ ]]; then
  echo "usage: $0 <full 40-character merge commit SHA on main>" >&2
  exit 2
fi

cd "$(git rev-parse --show-toplevel)"
git fetch -q origin main
if ! git cat-file -e "$new^{commit}" 2>/dev/null; then
  echo "$new is not a commit in this repository" >&2
  exit 1
fi
if ! git merge-base --is-ancestor "$new" origin/main; then
  echo "$new is not on origin/main; the protected ref must be a reviewed merge" >&2
  exit 1
fi

files=(.github/workflows/ci.yml .github/workflows/native-container-reuse.yml)
# Only the two protected reuse checkouts; other jobs pin their own reviewed refs.
pinned() {
  awk -v step="$2" '
    index($0, "- name: " step) { found = 1; next }
    found && /^ +- / { exit }
    found && $1 == "ref:" { print $2; exit }
  ' "$1"
}
old=$(printf '%s\n' \
  "$(pinned .github/workflows/ci.yml 'Check out protected verifier and policy')" \
  "$(pinned .github/workflows/native-container-reuse.yml 'Check out protected tools and profile')" | sort -u)
if [[ $(wc -l <<<"$old") -ne 1 || -z $old ]]; then
  echo "expected exactly one pinned protected ref across ${files[*]}, found:" >&2
  echo "$old" >&2
  exit 1
fi
if [[ $old == "$new" ]]; then
  echo "protected ref is already $new"
  exit 0
fi

perl -pi -e "s/\Q$old\E/$new/g" "${files[@]}" .github/warden/linux-test/README.md
echo "protected ref: $old -> $new"

# The receipts at the new ref must match the contract the caller now runs.
go test -count=1 -run '^TestProtectedReusePinsMatchContract$' ./scripts/
