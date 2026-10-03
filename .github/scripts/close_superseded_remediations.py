"""Close untouched generated snapshots only after a replacement PR exists."""

import json
import os
import re
import subprocess
from urllib.parse import urlencode


def api(path, *args):
    return json.loads(subprocess.check_output(["gh", "api", path, *args], text=True))


def candidate(pr, replacement, repository):
    return (
        pr["number"] != replacement["number"]
        and pr["state"] == "open"
        and pr["base"]["ref"] == replacement["base"]["ref"]
        and (pr["head"].get("repo") or {}).get("full_name") == repository
        and re.fullmatch(r"nox/remediate-\d+", pr["head"]["ref"]) is not None
        and pr["created_at"] < replacement["created_at"]
        and pr["title"] in {
            "chore(security): nox remediation (deps + actions)",
            "chore(deps): go mod tidy",
        }
    )


def untouched(commits):
    return len(commits) == 1 and all(
        commits[0]["commit"][role]["name"] == "nox-remediate"
        for role in ("author", "committer")
    )


def covered(files, tree):
    """Require artifact equality; a newer timestamp alone proves nothing."""
    if not files:
        return False
    for file in files:
        path = file["filename"]
        if file["status"] == "removed":
            if path in tree:
                return False
        elif tree.get(path) != file["sha"]:
            return False
        if file["status"] == "renamed" and file["previous_filename"] in tree:
            return False
    return True


def main():
    repository = os.environ["GITHUB_REPOSITORY"]
    owner = repository.split("/")[0]
    branch = "nox/remediate-" + os.environ["GITHUB_RUN_ID"]
    query = urlencode({"head": owner + ":" + branch, "state": "all"})
    replacements = api(f"repos/{repository}/pulls?{query}")
    if len(replacements) != 1:
        print("No unique replacement PR; keeping existing remediation PRs.")
        return
    replacement = replacements[0]
    if (replacement["head"].get("repo") or {}).get("full_name") != repository or (
        replacement["state"] != "open" and not replacement.get("merged_at")
    ):
        print("Replacement is unavailable; keeping existing remediation PRs.")
        return
    query = urlencode({"state": "open", "base": replacement["base"]["ref"], "per_page": 100})
    pages = api(f"repos/{repository}/pulls?{query}", "--paginate", "--slurp")
    snapshot = api(f"repos/{repository}/git/trees/{replacement['head']['sha']}?recursive=1")
    if snapshot.get("truncated"):
        print("Replacement tree is incomplete; keeping existing remediation PRs.")
        return
    tree = {entry["path"]: entry["sha"] for entry in snapshot["tree"]}
    for page in pages:
        for pr in page:
            if not candidate(pr, replacement, repository):
                continue
            number = pr["number"]
            commits = api(f"repos/{repository}/pulls/{number}/commits")
            if not untouched(commits):
                continue
            file_pages = api(f"repos/{repository}/pulls/{number}/files?per_page=100", "--paginate", "--slurp")
            if not covered([file for page in file_pages for file in page], tree):
                continue
            # Re-read before closing so a human update during cleanup is kept.
            current = api(f"repos/{repository}/pulls/{number}")
            if current["head"]["sha"] != pr["head"]["sha"] or not candidate(current, replacement, repository):
                continue
            latest = api(f"repos/{repository}/pulls/{replacement['number']}")
            if latest["head"]["sha"] != replacement["head"]["sha"] or (
                latest["state"] != "open" and not latest.get("merged_at")
            ):
                print("Replacement changed during cleanup; keeping remaining PRs.")
                return
            api(f"repos/{repository}/pulls/{number}", "--method", "PATCH", "-f", "state=closed")
            print(f"Closed #{number}; replaced by #{replacement['number']}.")


if __name__ == "__main__":
    main()
