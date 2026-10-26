#!/usr/bin/env python3
"""Check the CI suite filter on a real merge commit whose base moved."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

FILTER = Path(__file__).with_name("ci-changes.sh")


class ChangesTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(dir=os.environ.get("DYNA_SCRATCH"))
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name)
        self.env = {k: v for k, v in os.environ.items() if not k.startswith("GIT_")} | {
            "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"}

    def git(self, *args):
        return subprocess.run(["git", *args], cwd=self.repo, env=self.env, check=True, capture_output=True, text=True).stdout.strip()

    def commit(self, path, message):
        file = self.repo / path
        file.parent.mkdir(parents=True, exist_ok=True)
        file.write_text(message)
        self.git("add", "-A")
        self.git("commit", "-qm", message)

    def run_filter(self, event, base):
        env = self.env | {"EVENT": event, "BASE": base}
        out = subprocess.run(["bash", str(FILTER)], cwd=self.repo, env=env, check=True, capture_output=True, text=True).stdout
        return dict(line.split("=") for line in out.split())

    def pull_request(self, pr_file, main_file=None):
        """A merge commit of a PR branch into a main that has moved since the event's base."""
        self.git("init", "-q", "-b", "main")
        self.commit("README.md", "first")
        base = self.git("rev-parse", "HEAD")
        self.git("checkout", "-qb", "pr")
        self.commit(pr_file, "pr")
        self.git("checkout", "-q", "main")
        if main_file:
            self.commit(main_file, "main moved")
        self.git("checkout", "-q", "--detach")
        self.git("merge", "-q", "--no-ff", "-m", "merge", "pr")
        return base

    def test_docs_only_change_skips_go_when_main_moved(self):
        base = self.pull_request("docs/guide.md", main_file="internal/x/x.go")
        self.assertEqual(self.run_filter("pull_request", base), {"relay": "false", "go": "false", "computers": "false"})

    def test_go_change_runs_go(self):
        base = self.pull_request("internal/x/y.go", main_file="docs/z.md")
        self.assertEqual(self.run_filter("pull_request", base)["go"], "true")

    def test_empty_base_on_an_ordinary_commit_diffs_from_the_first_parent(self):
        self.pull_request("docs/guide.md")
        self.git("checkout", "-q", "pr")
        self.assertEqual(self.run_filter("pull_request", ""), {"relay": "false", "go": "false", "computers": "false"})

    def test_other_events_run_everything(self):
        base = self.pull_request("docs/guide.md")
        self.assertEqual(self.run_filter("push", base), {"relay": "true", "go": "true", "computers": "true"})


if __name__ == "__main__":
    unittest.main()
