#!/usr/bin/env python3
"""Check the CI suite filter on a real merge commit whose base moved."""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

FILTER = Path(__file__).with_name("ci-changes.sh")
NOTHING = {"relay": "false", "next": "false", "web": "false", "next_pi": "false"}
EVERYTHING = dict.fromkeys(NOTHING, "true")


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

    def pull_request(self, pr_file, main_file=None, large=False):
        """A merge commit of a PR branch into a main that has moved since the event's base."""
        self.git("init", "-q", "-b", "main")
        self.commit("README.md", "first")
        base = self.git("rev-parse", "HEAD")
        self.git("checkout", "-qb", "pr")
        self.commit(pr_file, "pr")
        if large:
            for i in range(2100):
                path = self.repo / "next/third_party/pi/packages/coding-agent/src" / f"file-{i}.ts"
                path.write_text("export {}\n")
            self.git("add", "-A")
            self.git("commit", "-qm", "large subtree")
        self.git("checkout", "-q", "main")
        if main_file:
            self.commit(main_file, "main moved")
        self.git("checkout", "-q", "--detach")
        self.git("merge", "-q", "--no-ff", "-m", "merge", "pr")
        return base

    def test_root_docs_change_skips_every_suite_when_main_moved(self):
        base = self.pull_request("LICENSE", main_file="next/engine/x.go")
        self.assertEqual(self.run_filter("pull_request", base), NOTHING)

    def test_go_change_runs_next_only(self):
        base = self.pull_request("next/engine/x.go", main_file="LICENSE")
        self.assertEqual(self.run_filter("pull_request", base), NOTHING | {"next": "true"})

    def test_empty_base_on_an_ordinary_commit_diffs_from_the_first_parent(self):
        self.pull_request("LICENSE")
        self.git("checkout", "-q", "pr")
        self.assertEqual(self.run_filter("pull_request", ""), NOTHING)

    def test_web_change_runs_web_and_next(self):
        base = self.pull_request("next/web/src/a.ts")
        self.assertEqual(self.run_filter("pull_request", base), NOTHING | {"next": "true", "web": "true"})

    def test_paths_read_outside_next_run_next(self):
        for path in ("testdata/connect/protocol.json", ".dyna/images/dyna/Containerfile", "install.sh"):
            with self.subTest(path=path), tempfile.TemporaryDirectory(dir=os.environ.get("DYNA_SCRATCH")) as tmp:
                self.repo = Path(tmp)
                base = self.pull_request(path)
                self.assertEqual(self.run_filter("pull_request", base)["next"], "true")

    def test_relay_change_runs_relay_only(self):
        base = self.pull_request("relay/src/worker.ts")
        self.assertEqual(self.run_filter("pull_request", base), NOTHING | {"relay": "true"})

    def test_shared_vectors_run_relay(self):
        base = self.pull_request("testdata/connect/protocol.json")
        self.assertEqual(self.run_filter("pull_request", base)["relay"], "true")

    def test_pi_change_runs_pi_and_next(self):
        base = self.pull_request("next/third_party/pi/packages/coding-agent/src/cli.ts", large=True)
        self.assertEqual(self.run_filter("pull_request", base), NOTHING | {"next": "true", "next_pi": "true"})

    def test_ci_change_runs_everything(self):
        for path in (".github/workflows/ci.yml", "next/scripts/ci.sh", "next/scripts/ci-changes.sh"):
            with self.subTest(path=path), tempfile.TemporaryDirectory(dir=os.environ.get("DYNA_SCRATCH")) as tmp:
                self.repo = Path(tmp)
                base = self.pull_request(path)
                self.assertEqual(self.run_filter("pull_request", base), EVERYTHING)

    def test_other_events_run_everything(self):
        base = self.pull_request("LICENSE")
        self.assertEqual(self.run_filter("push", base), EVERYTHING)


if __name__ == "__main__":
    unittest.main()
