import copy
import unittest
from unittest.mock import patch

import ci_report as report


class ReportTests(unittest.TestCase):
    def setUp(self):
        self.plan = {"version": 1, "sha": "a" * 40, "verification_ms": 17,
                     "jobs": [{"name": "unit", "action": "skip"}, {"name": "integration", "action": "run"}],
                     "run_jobs": ["integration"], "reusable_jobs": ["unit"]}
        self.current = {"id": 2, "workflow_id": 9, "status": "completed", "head_sha": "a" * 40,
                        "conclusion": "success", "created_at": "2026-10-23T00:00:00Z", "run_attempt": 1}
        self.baseline = dict(self.current, id=1, created_at="2026-10-22T00:00:00Z")
        self.mapping = {"unit": "Unit tests", "integration": "Integration"}
        self.jobs = [self.job("Unit tests", "skipped"), self.job("Integration", "success", 40)]
        self.old = [self.job("Unit tests", "success", 50), self.job("Integration", "success", 30)]

    @staticmethod
    def job(name, conclusion, seconds=0):
        return {"name": name, "status": "completed", "conclusion": conclusion,
                "started_at": "2026-10-23T00:00:00Z", "completed_at": f"2026-10-23T00:00:{seconds:02d}Z"}

    def build(self):
        return report.build_report(self.plan, self.current, self.baseline, self.jobs, self.old, self.mapping)

    def test_original_capture_matches_run_attempt_commit_and_precedes_jobs(self):
        capture = {"version": 1, "sha": self.plan["sha"], "run_id": 2,
                   "run_attempt": 1, "captured_at": "2026-10-23T00:00:00Z"}
        planner = dict(self.job("Capture shadow plan", "success"),
                       started_at="2026-10-23T00:00:00Z", completed_at="2026-10-23T00:00:00Z")
        jobs = self.jobs + [planner]
        self.assertEqual(report.validate_capture(capture, self.plan, self.current, jobs, self.mapping),
                         capture["captured_at"])
        for field, value in (("version", True), ("run_id", 3), ("run_attempt", 2),
                             ("sha", "b" * 40), ("captured_at", "2026-10-22T23:59:59Z"),
                             ("captured_at", "2026-10-23T00:00:01Z")):
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                report.validate_capture(dict(capture, **{field: value}), self.plan,
                                        self.current, jobs, self.mapping)
        for bad_jobs in ([], jobs + [self.jobs[0]],
                         [dict(self.jobs[0], started_at=None), self.jobs[1], planner]):
            with self.subTest(jobs=bad_jobs), self.assertRaises(ValueError):
                report.validate_capture(capture, self.plan, self.current, bad_jobs, self.mapping)

        for replacement in (dict(planner, conclusion="failure"),
                            dict(planner, completed_at="2026-10-23T00:00:01Z"),
                            dict(planner, started_at="2026-10-23T00:00:01Z")):
            with self.subTest(planner=replacement), self.assertRaises(ValueError):
                report.validate_capture(capture, self.plan, self.current,
                                        self.jobs + [replacement], self.mapping)
        with self.assertRaises(ValueError):
            report.validate_capture(capture, self.plan, self.current,
                                    jobs + [planner], self.mapping)

    def test_observation_and_counterfactual_are_distinct(self):
        got = self.build()
        self.assertEqual(got["observed_skipped_eligible_jobs"], ["unit"])
        self.assertEqual(got["observed_mapped_runner_seconds"], 40)
        self.assertEqual(got["observed_runner_seconds_difference"], 40)
        self.assertEqual(got["estimated_avoided_runner_seconds"], 50)
        self.assertEqual(got["verification_ms"], 17)
        self.assertNotIn("money_saved", got)

    def test_shadow_failure_has_no_avoided_time(self):
        self.plan.update(mode="shadow", eligible_jobs=["unit"], reusable_jobs=[], run_jobs=["integration", "unit"])
        self.plan["jobs"][0]["action"] = "run"
        self.jobs[0] = self.job("Unit tests", "failure", 50)
        got = self.build()
        self.assertEqual(got["eligible_failed_jobs"], ["unit"])
        self.assertEqual(got["estimated_avoided_runner_seconds"], 0)
        self.assertEqual(got["estimated_eligible_runner_seconds"], 50)
        # An unrelated skipped job in shadow mode still earns no avoided time.
        self.jobs[0] = self.job("Unit tests", "skipped")
        self.assertEqual(self.build()["estimated_avoided_runner_seconds"], 0)

    def test_missing_or_matrix_ambiguity_is_unknown(self):
        for missing in (True, False):
            with self.subTest(missing=missing):
                self.jobs = [self.job("Integration", "success", 40)]
                if not missing:
                    self.jobs += [self.job("Unit tests", "success", 20)] * 2
                got = self.build()
                self.assertIsNone(got["observed_mapped_runner_seconds"])
                self.assertIsNone(got["estimated_avoided_runner_seconds"])
                self.assertIsNone(got["observed_runner_seconds_difference"])
                self.assertEqual(got["jobs"][1]["measurement"], "missing_or_ambiguous")

    def test_missing_baseline_is_not_zero(self):
        self.old = []
        self.assertIsNone(self.build()["estimated_avoided_runner_seconds"])
        self.assertIsNone(self.build()["baseline_mapped_runner_seconds"])

    def test_negative_difference_is_preserved(self):
        self.jobs[0] = self.job("Unit tests", "success", 59)
        self.assertEqual(self.build()["observed_runner_seconds_difference"], -19)

    def test_refuses_wrong_context_and_inconsistent_plans(self):
        cases = [
            ("current", "status", "in_progress"), ("baseline", "conclusion", "failure"),
            ("baseline", "workflow_id", 10), ("baseline", "id", 2),
            ("baseline", "created_at", "2026-10-24T00:00:00Z"),
            ("plan", "sha", "b" * 40), ("plan", "reusable_jobs", []),
            ("plan", "verification_ms", True), ("plan", "mode", "shadow"),
            ("plan", "eligible_jobs", []), ("plan", "version", 2), ("plan", "version", True),
        ]
        for obj, key, value in cases:
            with self.subTest(obj=obj, key=key):
                original = copy.deepcopy(getattr(self, obj))
                getattr(self, obj)[key] = value
                with self.assertRaises(ValueError):
                    self.build()
                setattr(self, obj, original)
        self.mapping["integration"] = "Unit tests"
        with self.assertRaises(ValueError):
            self.build()

    def test_duplicate_json_keys_refused(self):
        with self.assertRaises(ValueError):
            report.decode('{"sha":"a","sha":"b"}')

    def test_attempt_pagination_and_rerun_race(self):
        first = dict(self.current, run_attempt=2)
        jobs = [dict(self.job("Unit tests", "success"), run_id=2, run_attempt=2)]
        responses = [first, {"total_count": 2, "jobs": jobs},
                     {"total_count": 2, "jobs": [dict(jobs[0], name="Integration")]}, first]
        with patch.object(report, "api", side_effect=responses) as api:
            _, got = report.snapshot("owner/repo", 2)
            self.assertEqual(len(got), 2)
            self.assertIn('/attempts/2/jobs?per_page=100&page=2', api.call_args_list[2].args[0])
        with patch.object(report, "api", side_effect=[first, {"total_count": 1, "jobs": jobs}, dict(first, run_attempt=3)]):
            with self.assertRaisesRegex(ValueError, 'changed'):
                report.snapshot("owner/repo", 2)
        jobs[0]["run_attempt"] = 1
        with patch.object(report, "api", side_effect=[first, {"total_count": 1, "jobs": jobs}, first]):
            with self.assertRaisesRegex(ValueError, 'different'):
                report.snapshot("owner/repo", 2)


if __name__ == '__main__':
    unittest.main()
