import copy
import unittest
from unittest.mock import patch

import close_superseded_remediations as cleanup


def pr(number, branch, created):
    return {"number": number, "state": "open", "title": "chore(security): nox remediation (deps + actions)",
            "base": {"ref": "main"}, "head": {"ref": branch, "sha": str(number), "repo": {"full_name": "klarlabs-studio/warden"}},
            "created_at": created}


class CleanupTests(unittest.TestCase):
    def test_scope(self):
        newest = pr(10, "nox/remediate-200", "2026-10-03T00:00:00Z")
        older = pr(9, "nox/remediate-100", "2026-10-02T00:00:00Z")
        self.assertTrue(cleanup.candidate(older, newest, "klarlabs-studio/warden"))
        for path, value in [("base", {"ref": "other"}), ("head", {"ref": "feature", "repo": {"full_name": "fork/warden"}}),
                            ("title", "Human PR"), ("state", "closed"), ("created_at", "2026-10-04T00:00:00Z")]:
            changed = copy.deepcopy(older)
            changed[path] = value
            self.assertFalse(cleanup.candidate(changed, newest, "klarlabs-studio/warden"))
        self.assertFalse(cleanup.candidate(newest, newest, "klarlabs-studio/warden"))

    def test_human_changes_are_kept(self):
        generated = {"commit": {"author": {"name": "nox-remediate"}, "committer": {"name": "nox-remediate"}}}
        self.assertTrue(cleanup.untouched([generated]))
        self.assertFalse(cleanup.untouched([generated, generated]))
        changed = copy.deepcopy(generated)
        changed["commit"]["committer"]["name"] = "human"
        self.assertFalse(cleanup.untouched([changed]))

    def test_no_replacement_closes_nothing(self):
        with patch.dict("os.environ", GITHUB_REPOSITORY="klarlabs-studio/warden", GITHUB_RUN_ID="200"), patch.object(cleanup, "api", return_value=[]) as api:
            cleanup.main()
        self.assertEqual(api.call_count, 1)

    def test_only_untouched_older_snapshot_is_closed(self):
        newest = pr(10, "nox/remediate-200", "2026-10-03T00:00:00Z")
        older = pr(9, "nox/remediate-100", "2026-10-02T00:00:00Z")
        human = pr(8, "nox/remediate-99", "2026-10-01T00:00:00Z")
        commits = [{"commit": {"author": {"name": "nox-remediate"}, "committer": {"name": "nox-remediate"}}}]
        closed = []

        def api(path, *args):
            if "--method" in args:
                closed.append(path)
                return {}
            if "head=" in path:
                return [newest]
            if "--paginate" in args:
                if "/files?" in path:
                    return [[{"filename": "go.mod", "status": "modified", "sha": "blob"}]]
                return [[older, human]]
            if "/git/trees/" in path:
                return {"truncated": False, "tree": [{"path": "go.mod", "sha": "blob"}]}
            if path.endswith("/9/commits"):
                return commits
            if path.endswith("/8/commits"):
                return commits + commits
            if path.endswith("/9"):
                return older
            if path.endswith("/10"):
                return newest
            self.fail(path)

        with patch.dict("os.environ", GITHUB_REPOSITORY="klarlabs-studio/warden", GITHUB_RUN_ID="200"), patch.object(cleanup, "api", side_effect=api):
            cleanup.main()
        self.assertEqual(closed, ["repos/klarlabs-studio/warden/pulls/9"])

    def test_newer_snapshot_must_contain_older_changes(self):
        files = [{"filename": "go.mod", "status": "modified", "sha": "fixed"}]
        self.assertTrue(cleanup.covered(files, {"go.mod": "fixed"}))
        self.assertFalse(cleanup.covered(files, {"go.mod": "tidy-only"}))
        self.assertFalse(cleanup.covered(files, {}))
        removed = [{"filename": "old.yml", "status": "removed"}]
        self.assertTrue(cleanup.covered(removed, {}))
        self.assertFalse(cleanup.covered(removed, {"old.yml": "blob"}))
        renamed = [{"filename": "new.yml", "status": "renamed", "sha": "blob", "previous_filename": "old.yml"}]
        self.assertTrue(cleanup.covered(renamed, {"new.yml": "blob"}))
        self.assertFalse(cleanup.covered(renamed, {"new.yml": "blob", "old.yml": "blob"}))


if __name__ == "__main__":
    unittest.main()
