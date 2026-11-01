#!/usr/bin/env python3
"""Check when the pool watcher cancels a run, against a scripted GitHub."""
from datetime import datetime, timezone
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("watch", Path(__file__).with_name("ci-pool-watch.py"))
watcher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(watcher)

START = 1_000_000
STALL = 20 * 60
RUN = 7


def stamp(at):
    return datetime.fromtimestamp(at, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ") if at else None


def job(name, status, started=None, completed=None, runner=None, labels=("dyna",)):
    return {"name": name, "status": status, "labels": list(labels), "runner_name": runner,
            "started_at": stamp(started), "completed_at": stamp(completed)}


class GitHub:
    """Answers from a function of the clock, and the clock moves only by sleeping."""

    def __init__(self, own, others=lambda now: {}):
        self.now = START
        self.own, self.others = own, others
        self.cancelled = None
        self.calls = 0

    def api(self, path):
        self.calls += 1
        answer = self.answer(path)
        return None if answer is None else answer

    def answer(self, path):
        if "/attempts/" in path:
            jobs = self.own(self.now)
            return None if jobs is None else {"jobs": jobs}
        others = self.others(self.now)
        if others is None:
            return None
        if "actions/runs?" in path:
            return {"workflow_runs": [{"id": id} for id in sorted(others) + [RUN]]}
        id = int(path.split("/runs/")[1].split("/")[0])
        return {"jobs": others.get(id, self.own(self.now) or [])}

    def run(self, limit=4 * 3600):
        def sleep(seconds):
            self.now += seconds
            if self.now - START > limit:
                raise TimeoutError

        def cancel():
            self.cancelled = self.now - START

        try:
            return watcher.watch(self.api, cancel, lambda: self.now, sleep, "o/r", RUN, 1, "pool-watch", STALL)
        except TimeoutError:
            return None


def queued(now):
    return [job("changes", "completed", START, START + 30, "GitHub Actions 1", ["ubuntu-latest"]), job("next", "queued", START + 30)]


class WatchTest(unittest.TestCase):
    def test_dead_pool_cancels_soon_after_the_stall(self):
        github = GitHub(queued)
        self.assertEqual(github.run(), 1)
        self.assertGreaterEqual(github.cancelled, STALL)
        self.assertLessEqual(github.cancelled, STALL + 2 * watcher.POLL + watcher.QUIET_RECHECK)

    def test_busy_pool_is_not_a_stuck_job(self):
        """Another run holds the runner, for far longer than the stall, and then this job gets it."""
        def own(now):
            if now - START < 3 * STALL:
                return queued(now)
            return [job("next", "completed", START + 3 * STALL, START + 3 * STALL + 60, "dyna-ci-pve-1")]

        def others(now):
            return {3: [job("next", "in_progress", now - 60, None, "dyna-ci-pve-2")]}

        github = GitHub(own, others)
        self.assertEqual(github.run(), 0)
        self.assertIsNone(github.cancelled)

    def test_a_moment_between_jobs_is_not_a_dead_pool(self):
        """A busy pool shows nothing for one look. The next look must save the run."""
        def others(now):
            look = (now - START) // watcher.QUIET_RECHECK
            return {} if look % 2 else {3: [job("next", "in_progress", now - 60, None, "dyna-ci-pve-2")]}

        github = GitHub(queued, others)
        self.assertIsNone(github.run(limit=2 * 3600))
        self.assertIsNone(github.cancelled)

    def test_a_job_that_just_finished_shows_the_pool_works(self):
        def others(now):
            return {3: [job("next", "completed", now - 200, now - 100, "dyna-ci-pve-2")]}

        github = GitHub(queued, others)
        self.assertIsNone(github.run(limit=2 * 3600))
        self.assertIsNone(github.cancelled)

    def test_old_finished_jobs_and_other_runners_prove_nothing(self):
        def others(now):
            return {
                3: [job("next", "completed", START - 9000, START - 8000, "dyna-ci-pve-2")],
                4: [job("changes", "in_progress", now - 60, None, "GitHub Actions 2", ["ubuntu-latest"])],
                5: [job("next", "queued", now - 60)],
                # GitHub stamps a job cancelled in the queue, and it never held a runner.
                6: [job("next", "completed", now - 90, now - 60, None)],
            }

        self.assertEqual(GitHub(queued, others).run(), 1)

    def test_a_run_making_progress_is_left_alone(self):
        def own(now):
            return [job("next", "completed", START, now - 60, "dyna-ci-pve-1"), job("next-web", "queued", START)]

        github = GitHub(own)
        self.assertIsNone(github.run(limit=2 * 3600))
        self.assertEqual(github.calls, 2 * 3600 // watcher.POLL + 1)

    def test_github_failing_never_cancels(self):
        github = GitHub(queued, lambda now: None)
        self.assertIsNone(github.run(limit=2 * 3600))
        self.assertIsNone(github.cancelled)

    def test_a_watcher_that_loses_its_own_jobs_keeps_looking(self):
        blank = {"gone": False}

        def own(now):
            blank["gone"] = not blank["gone"]
            return None if blank["gone"] else queued(now)

        self.assertEqual(GitHub(own).run(), 1)

    def test_nothing_for_the_pool_ends_the_watch(self):
        github = GitHub(lambda now: [job("changes", "completed", START, START + 30, "x", ["ubuntu-latest"])])
        self.assertEqual(github.run(), 0)
        self.assertIsNone(github.cancelled)

    def test_finished_pool_jobs_end_the_watch(self):
        github = GitHub(lambda now: [job("next", "completed", START, START + 60, "dyna-ci-pve-1")])
        self.assertEqual(github.run(), 0)

    def test_a_running_pool_job_with_nothing_queued_keeps_the_watch(self):
        github = GitHub(lambda now: [job("next", "in_progress", START, None, "dyna-ci-pve-1")])
        self.assertIsNone(github.run(limit=3600))
        self.assertIsNone(github.cancelled)

    def test_a_watcher_never_counts_its_own_job(self):
        def own(now):
            return queued(now) + [job("pool-watch", "in_progress", now, None, "GitHub Actions 1", ["ubuntu-latest"])]

        self.assertEqual(GitHub(own).run(), 1)


if __name__ == "__main__":
    unittest.main()
