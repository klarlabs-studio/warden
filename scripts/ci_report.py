#!/usr/bin/env python3
"""Read-only GitHub CI measurement. Requires Python 3.9+ and authenticated gh.

No report field authorizes reuse. Baseline durations are counterfactual estimates,
not billing measurements or proof that Warden caused a skipped job.
"""
import argparse
import datetime
import json
import re
import subprocess
import sys
from pathlib import Path


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key: " + key)
        result[key] = value
    return result


def decode(text):
    return json.loads(text, object_pairs_hook=unique_object)


def api(endpoint):
    return decode(subprocess.check_output(["gh", "api", endpoint], text=True))


def snapshot(repository, run_id):
    endpoint = f"repos/{repository}/actions/runs/{run_id}"
    run = api(endpoint)
    if run.get("status") != "completed":
        raise ValueError("run must be completed before reporting")
    attempt = run["run_attempt"]
    jobs = []
    page = 1
    while True:
        data = api(f"{endpoint}/attempts/{attempt}/jobs?per_page=100&page={page}")
        batch = data["jobs"]
        jobs.extend(batch)
        if len(jobs) == data["total_count"]:
            break
        if not batch or len(jobs) > data["total_count"]:
            raise ValueError("incomplete jobs inventory")
        page += 1
    after = api(endpoint)
    if after.get("status") != "completed" or after["run_attempt"] != attempt:
        raise ValueError("run changed during measurement")
    if any(j.get("run_id") != run_id or j.get("run_attempt") != attempt for j in jobs):
        raise ValueError("job belongs to a different run or attempt")
    return run, jobs


def timestamp(value):
    date = datetime.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if date.tzinfo is None:
        raise ValueError("timestamp must include timezone")
    return date


def duration(job):
    if job.get("conclusion") == "skipped":
        return 0
    if job.get("status") != "completed" or not job.get("started_at") or not job.get("completed_at"):
        return None
    seconds = (timestamp(job["completed_at"]) - timestamp(job["started_at"])).total_seconds()
    if seconds < 0:
        raise ValueError("negative job duration")
    return seconds


def build_report(plan, current, baseline, jobs, baseline_jobs, mapping):
    if current["status"] != "completed" or baseline["status"] != "completed":
        raise ValueError("both runs must be completed")
    if current["id"] == baseline["id"] or current["workflow_id"] != baseline["workflow_id"]:
        raise ValueError("baseline must be another run of the same workflow")
    if baseline["conclusion"] != "success" or timestamp(baseline["created_at"]) >= timestamp(current["created_at"]):
        raise ValueError("baseline must be an earlier successful run")
    if type(plan["version"]) is not int or plan["version"] != 1 or plan["sha"] != current["head_sha"]:
        raise ValueError("plan version or commit mismatch")
    mode = plan.get("mode", "enforce")
    names = [j["name"] for j in plan["jobs"]]
    if mode not in ("shadow", "enforce") or not names or len(set(names)) != len(names):
        raise ValueError("invalid plan inventory")
    if any(not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,127}", n) for n in names):
        raise ValueError("invalid policy job name")
    if any(j["action"] not in ("run", "skip") for j in plan["jobs"]):
        raise ValueError("invalid plan action")
    reusable = sorted(j["name"] for j in plan["jobs"] if j["action"] == "skip")
    if plan["reusable_jobs"] != reusable or plan["run_jobs"] != sorted(set(names) - set(reusable)):
        raise ValueError("inconsistent plan partition")
    eligible = plan.get("eligible_jobs", reusable)
    if not isinstance(eligible, list) or len(eligible) != len(set(eligible)) or not set(eligible) <= set(names):
        raise ValueError("invalid eligibility inventory")
    if (mode == "enforce" and sorted(eligible) != reusable) or (mode == "shadow" and reusable):
        raise ValueError("mode contradicts decisions")
    if set(mapping) != set(names) or any(not isinstance(v, str) or not v for v in mapping.values()) or len(set(mapping.values())) != len(mapping):
        raise ValueError("map every policy job to a distinct exact GitHub job name")
    verification_ms = plan["verification_ms"]
    if type(verification_ms) is not int or verification_ms < 0:
        raise ValueError("invalid verification duration")
    rows = []
    for name in sorted(names):
        matches = [j for j in jobs if j["name"] == mapping[name]]
        previous = [j for j in baseline_jobs if j["name"] == mapping[name]]
        job = matches[0] if len(matches) == 1 else None
        old = previous[0] if len(previous) == 1 else None
        if job and job["status"] != "completed":
            raise ValueError("job has not completed")
        elapsed = duration(job) if job else None
        historical = duration(old) if old and old.get("conclusion") == "success" else None
        skipped = job is not None and job.get("conclusion") == "skipped"
        rows.append({"name": name, "github_job": mapping[name], "eligible": name in eligible,
                     "conclusion": job.get("conclusion") if job else None,
                     "measurement": "observed" if job else "missing_or_ambiguous",
                     "observed_runner_seconds": elapsed, "baseline_runner_seconds": historical,
                     "estimated_avoided_runner_seconds": historical if skipped and name in reusable else None})
    def total(field):
        values = [row[field] for row in rows]
        return sum(values) if all(v is not None for v in values) else None
    observed = total("observed_runner_seconds")
    before = total("baseline_runner_seconds")
    eligible_durations = [r["baseline_runner_seconds"] for r in rows if r["eligible"]]
    unresolved_reuse = any(r["name"] in reusable and r["measurement"] != "observed" for r in rows)
    estimated = [r["estimated_avoided_runner_seconds"] for r in rows if r["name"] in reusable and r["conclusion"] == "skipped"]
    return {"version": 1, "sha": plan["sha"], "mode": mode,
            "run_id": current["id"], "run_attempt": current["run_attempt"],
            "baseline_run_id": baseline["id"], "baseline_run_attempt": baseline["run_attempt"],
            "verification_ms": verification_ms, "jobs": rows,
            "observed_skipped_eligible_jobs": [r["name"] for r in rows if r["eligible"] and r["conclusion"] == "skipped"],
            "eligible_failed_jobs": [r["name"] for r in rows if r["eligible"] and r["conclusion"] in ("failure", "timed_out")],
            "observed_mapped_runner_seconds": observed,
            "baseline_mapped_runner_seconds": before,
            "observed_runner_seconds_difference": before - observed if before is not None and observed is not None else None,
            "estimated_eligible_runner_seconds": sum(eligible_durations) if all(v is not None for v in eligible_durations) else None,
            "estimated_avoided_runner_seconds": sum(estimated) if not unresolved_reuse and all(v is not None for v in estimated) else None,
            "limitations": ["Historical durations estimate counterfactual work; they do not measure billing or prove Warden caused a skip.",
                            "Totals cover mapped jobs only, excluding planner, setup outside those jobs, local validation and other workflow work.",
                            "Runner classes, changed workload, caches and unrelated skip conditions can change the comparison.",
                            "A shadow run authorizes no reuse and claims no avoided runner time."]}


def observe_test_reuse(plan, current, baseline, jobs, previous):
    """Corroborate the advisory plan with actual workflow step conclusions."""
    measured = build_report(plan, current, baseline, jobs, previous,
                            {"test": "ci / Test (ubuntu-latest)"})
    verifiers = [j for j in jobs if j["name"] == "Verify protected test reuse"]
    tests = [j for j in jobs if j["name"] == "ci / Test (ubuntu-latest)"]
    observed = False
    verifier_seconds = None
    if len(verifiers) == len(tests) == 1:
        verifier, test = verifiers[0], tests[0]
        verifier_seconds = duration(verifier)
        steps = test.get("steps", [])
        def concluded(name, conclusion):
            matches = [s for s in steps if s["name"] == name]
            return (len(matches) == 1 and matches[0].get("status") == "completed"
                    and matches[0].get("conclusion") == conclusion)
        observed = (plan.get("mode") == "enforce" and plan["reusable_jobs"] == ["test"]
                    and verifier.get("status") == "completed"
                    and verifier.get("conclusion") == test.get("conclusion") == "success"
                    and test.get("status") == "completed"
                    and verifier_seconds is not None and duration(test) is not None
                    and timestamp(verifier["completed_at"]) <= timestamp(test["started_at"])
                    and concluded("Record caller-authorized reuse", "success")
                    and concluded("Run signed offline test contract", "skipped")
                    and concluded("Test (race + coverage)", "skipped")
                    and concluded("Coverage gate", "skipped"))
    row = measured["jobs"][0]
    return {"observed": observed,
            "test_wrapper_runner_seconds": row["observed_runner_seconds"],
            "protected_verifier_runner_seconds": verifier_seconds,
            "historical_test_runner_seconds": row["baseline_runner_seconds"],
            "historical_test_runner_seconds_difference": (
                measured["observed_runner_seconds_difference"] if observed else None),
            "limitations": ["Step conclusions corroborate an advisory artifact; this report grants no execution authority.",
                            "Historical differences are not billing savings; verifier, local validation and other work are separate costs."]}


def validate_capture(capture, plan, current, jobs, mapping):
    """Advisory timing check, never an authentication or reuse decision."""
    for field, expected in (("version", 1), ("run_id", current["id"]),
                            ("run_attempt", current["run_attempt"])):
        if type(capture[field]) is not int or capture[field] != expected:
            raise ValueError("original plan capture " + field + " mismatch")
    if capture["sha"] != current["head_sha"] or capture["sha"] != plan["sha"]:
        raise ValueError("original plan capture commit mismatch")
    captured = timestamp(capture["captured_at"])
    if captured < timestamp(current["created_at"]):
        raise ValueError("original plan predates workflow run")
    planners = [job for job in jobs if job["name"] == "Capture shadow plan"]
    if len(planners) != 1 or planners[0].get("conclusion") != "success":
        raise ValueError("original planner job missing, ambiguous or unsuccessful")
    planner = planners[0]
    finished = timestamp(planner["completed_at"])
    if not timestamp(planner["started_at"]) <= captured <= finished:
        raise ValueError("capture timestamp outside planner job")
    # The GitHub job boundary independently corroborates the ordering; the
    # artifact's self-reported timestamp alone is insufficient.
    # Missing, ambiguous and skipped jobs cannot establish pre-execution
    # timing. Retain that uncertainty without discarding the other observations.
    unavailable = []
    for name in mapping.values():
        matches = [job for job in jobs if job["name"] == name]
        if (len(matches) != 1 or matches[0].get("conclusion") == "skipped"
                or not matches[0].get("started_at")):
            unavailable.append(name)
            continue
        if finished > timestamp(matches[0]["started_at"]):
            raise ValueError("original plan captured after job started: " + name)
    return {"plan_captured_at": capture["captured_at"],
            "capture_timing_unavailable_jobs": sorted(unavailable),
            "plan_source": "capture_timing_unavailable" if unavailable else "pre_ci_capture"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--run", type=int, required=True)
    parser.add_argument("--baseline", type=int, required=True)
    parser.add_argument("--plan", type=Path, required=True)
    parser.add_argument("--capture", type=Path, help="Original pre-CI capture metadata; omission labels the plan retrospective")
    parser.add_argument("--jobs", type=Path, required=True, help="JSON object mapping policy jobs to exact GitHub job names")
    parser.add_argument("--reuse-plan", type=Path, help="Same-run protected test plan; corroborated against observed job steps")
    args = parser.parse_args()
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", args.repository) or min(args.run, args.baseline) <= 0:
        parser.error("invalid repository or run ID")
    try:
        plan, mapping = decode(args.plan.read_text()), decode(args.jobs.read_text())
        current, jobs = snapshot(args.repository, args.run)
        baseline, previous = snapshot(args.repository, args.baseline)
        report = build_report(plan, current, baseline, jobs, previous, mapping)
        report["plan_source"] = "retrospective"
        if args.capture:
            report.update(validate_capture(
                decode(args.capture.read_text()), plan, current, jobs, mapping))
            report["planner_runner_seconds"] = duration(next(
                job for job in jobs if job["name"] == "Capture shadow plan"))
            report["limitations"].append("The original artifact and planner timing are advisory measurements, not authenticated execution evidence or skip authorization.")
        if args.reuse_plan:
            report["protected_test_reuse"] = observe_test_reuse(
                decode(args.reuse_plan.read_text()), current, baseline, jobs, previous)
        report["repository"] = args.repository
        print(json.dumps(report, sort_keys=True, allow_nan=False))
    except (ValueError, KeyError, TypeError, AttributeError, OSError, subprocess.CalledProcessError) as error:
        print("warden-report: " + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
