#!/usr/bin/env python3
"""Check the go-test slot hooks with real flocks and real processes."""
import os
import re
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import unittest

SLOT = Path(__file__).with_name("ci-runner-slot.sh")


class SlotTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory(dir=os.environ.get("DYNA_SCRATCH"))
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.slots = self.root / "slots"
        self.hooks = self.root / "runner"
        self.hooks.mkdir()
        for name in ("ci-runner-slot.sh", "ci-runner-slot-release.sh"):
            shutil.copy(SLOT, self.hooks / name)
        # The hook finds the job process by its name, as it does under the real runner.
        (self.root / "Runner.Worker").symlink_to(shutil.which("bash"))
        self.workers = []
        self.addCleanup(self.stop_workers)

    def stop_workers(self):
        # Each fixture owns its whole process group. Killing only Runner.Worker
        # leaves queued hook children writing into a deleted temporary folder.
        for worker in self.workers:
            try:
                os.killpg(worker.pid, signal.SIGKILL)
            except ProcessLookupError:
                pass
            worker.wait(timeout=30)
            if worker.stdin is not None:
                worker.stdin.close()
            worker.stdout.close()

    def env(self, job="test", workspace="/work/a", **extra):
        return os.environ | {
            "CI_SLOT_DIR": str(self.slots), "CI_SLOT_POLL": "0.02", "CI_SLOT_WAIT": "0",
            "GITHUB_JOB": job, "GITHUB_WORKSPACE": workspace,
        } | extra

    def worker(self, hook="ci-runner-slot.sh", **kwargs):
        """Runs a hook under a fake job process, which then stays alive until killed."""
        script = f'bash {self.hooks / hook}; echo hook-done $?; read -r _'
        proc = subprocess.Popen([str(self.root / "Runner.Worker"), "-c", script], env=self.env(**kwargs),
                                stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, start_new_session=True)
        self.workers.append(proc)
        out = []
        while True:  # the worker prints once the hook has returned
            line = self.line(proc)
            self.assertTrue(line, "worker ended before hook-done")
            out.append(line)
            if line.startswith("hook-done"):
                break
        return proc, "".join(out)

    def lock_freed(self, n=1):
        """True once the slot's flock can be taken. Blocks until then, bounded."""
        return subprocess.run(["flock", "-w", "30", str(self.slots / f"slot.{n}"), "true"]).returncode == 0

    def test_job_takes_the_slot_and_a_second_job_gives_up_with_a_message(self):
        first, out = self.worker()
        self.assertIn("go-test slot 1 of 1 taken", out)
        self.assertIn("hook-done 0", out)
        second, out = self.worker(workspace="/work/b")
        self.assertIn("hook-done 1", out)
        self.assertEqual((self.slots / "slot.1.owner").read_text().strip(), "/work/a")

    def test_slot_is_free_when_the_job_process_dies(self):
        first, _ = self.worker()
        first.kill()  # a crashed job runs no hook
        first.wait()  # the runner reaps its dead worker; a zombie would still look alive
        self.assertTrue(self.lock_freed())
        second, out = self.worker(workspace="/work/b")
        self.assertIn("go-test slot 1 of 1 taken", out)

    def test_release_hook_frees_only_its_own_slot(self):
        first, _ = self.worker()
        other, out = self.worker("ci-runner-slot-release.sh", workspace="/work/b")
        self.assertIn("hook-done 0", out)
        self.assertEqual((self.slots / "slot.1.owner").read_text().strip(), "/work/a")
        mine, out = self.worker("ci-runner-slot-release.sh")
        self.assertIn("hook-done 0", out)
        self.assertTrue(self.lock_freed())

    def test_waiting_job_gets_the_slot_when_the_holder_ends(self):
        first, _ = self.worker()
        waiter = subprocess.Popen([str(self.root / "Runner.Worker"), "-c", f"bash {self.hooks / 'ci-runner-slot.sh'}"],
                                  env=self.env(workspace="/work/b", CI_SLOT_WAIT="30"), stdout=subprocess.PIPE, text=True, start_new_session=True)
        self.workers.append(waiter)
        self.assertEqual(self.line(waiter).strip(), "waiting for a go-test slot")
        first.kill()
        first.wait()
        self.assertEqual(waiter.wait(timeout=30), 0)

    def test_waiters_take_the_slot_in_the_order_they_arrived(self):
        first, _ = self.worker()

        def waiter(workspace, poll):
            proc = subprocess.Popen([str(self.root / "Runner.Worker"), "-c", f"bash {self.hooks / 'ci-runner-slot.sh'}; read -r _"],
                                    env=self.env(workspace=workspace, CI_SLOT_WAIT="30", CI_SLOT_POLL=poll),
                                    stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, start_new_session=True)
            self.workers.append(proc)
            self.assertEqual(self.line(proc).strip(), "waiting for a go-test slot")
            return proc

        # The older waiter polls slowly and the newer one quickly: without a queue the newer one wins.
        older = waiter("/work/older", "0.3")
        waiter("/work/newer", "0.01")
        first.kill()
        first.wait()
        self.assertEqual(self.line(older).strip(), "go-test slot 1 of 1 taken")
        self.assertEqual((self.slots / "slot.1.owner").read_text().strip(), "/work/older")

    def test_a_main_run_goes_ahead_of_waiting_pull_requests(self):
        first, _ = self.worker()

        def waiter(workspace, ref):
            proc = subprocess.Popen([str(self.root / "Runner.Worker"), "-c", f"bash {self.hooks / 'ci-runner-slot.sh'}; read -r _"],
                                    env=self.env(workspace=workspace, CI_SLOT_WAIT="30", GITHUB_REF=ref),
                                    stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, start_new_session=True)
            self.workers.append(proc)
            self.assertEqual(self.line(proc).strip(), "waiting for a go-test slot")
            return proc

        waiter("/work/pr", "refs/pull/7/merge")
        main = waiter("/work/main", "refs/heads/main")
        first.kill()
        first.wait()
        self.assertEqual(self.line(main).strip(), "go-test slot 1 of 1 taken")
        self.assertEqual((self.slots / "slot.1.owner").read_text().strip(), "/work/main")

    def test_keeper_outlasts_the_job_timeout_and_the_wait_ends_inside_it(self):
        script = SLOT.read_text()
        workflow = (SLOT.parent.parent / ".github/workflows/ci.yml").read_text()
        job = workflow[workflow.index("  go-shard:\n"):]
        timeout = int(re.search(r"timeout-minutes: (\d+)", job).group(1)) * 60
        hold = int(re.search(r"CI_SLOT_HOLD:-(\d+)", script).group(1))
        wait = int(re.search(r"CI_SLOT_WAIT:-(\d+)", script).group(1))
        self.assertGreater(hold, timeout, "a long job would lose its slot to the next one")
        self.assertLess(wait, timeout, "GitHub would cancel a waiting job before the hook gives up")

    def test_keeper_lets_go_of_a_live_worker_at_the_hold_limit(self):
        first, _ = self.worker(CI_SLOT_HOLD="0")
        self.assertIsNone(first.poll())  # the job process is still alive
        self.assertTrue(self.lock_freed())

    def test_two_slots_let_two_jobs_run(self):
        self.worker(CI_GO_TEST_SLOTS="2")
        _, out = self.worker(workspace="/work/b", CI_GO_TEST_SLOTS="2")
        self.assertIn("go-test slot 2 of 2 taken", out)

    def test_unwritable_slot_folder_does_not_block_the_job(self):
        self.slots.mkdir()
        self.slots.chmod(0o555)  # a folder another user created, as root once did on a runner host
        self.addCleanup(self.slots.chmod, 0o755)
        if os.access(self.slots, os.W_OK):
            self.skipTest("this user can write to read-only folders")
        _, out = self.worker()
        self.assertIn("hook-done 0", out)

    def test_other_jobs_take_no_slot(self):
        _, out = self.worker(job="frontend")
        self.assertIn("hook-done 0", out)
        self.assertFalse(self.slots.exists())

    def container_fixture(self):
        import queue
        import threading

        self.events = queue.Queue()
        self.bin = self.root / "bin"
        self.bin.mkdir()
        fifo = self.root / "events"
        os.mkfifo(fifo)
        # Keep one writer open so the reader has no EOF between Docker calls.
        self.event_fd = os.open(fifo, os.O_RDWR)
        self.addCleanup(os.close, self.event_fd)
        def read_events():
            with os.fdopen(os.dup(self.event_fd)) as stream:
                for line in stream:
                    import json
                    event = json.loads(line)
                    self.events.put(event)
                    if event[0] == "reader-stop":
                        return
        reader = threading.Thread(target=read_events, daemon=True)
        reader.start()

        docker = self.bin / "docker"
        docker.write_text(r"""#!/usr/bin/env python3
import json, os, pathlib, signal, sys
root = pathlib.Path(os.environ["SLOT_DOCKER_ROOT"])
args = sys.argv[1:]
(root / ("process." + str(os.getpid()))).touch()
def event(kind, name):
    with (root / "events").open("w") as stream:
        stream.write(json.dumps([kind, name, os.getpid()]) + "\n")
if args[0] == "create":
    name = args[args.index("--name") + 1]
    (root / (name + ".created")).touch()
    event("create", name)
    print(name)
elif args[0] == "start":
    name = args[-1]
    (root / (name + ".running")).touch()
    (root / (name + ".client")).write_text(str(os.getpid()))
    event("start", name)
    with (root / (name + ".exit")).open() as stream:
        code = int(stream.read())
    (root / (name + ".running")).unlink(missing_ok=True)
    event("exit", name)
    sys.exit(code)
elif args[:2] == ["rm", "-f"]:
    name = args[-1]
    event("remove", name)
    if (root / "hold-cleanup").exists():
        with (root / (name + ".remove")).open() as stream:
            stream.read()
    if (root / "daemon-error").exists():
        sys.exit(1)
    client = root / (name + ".client")
    if client.exists():
        try: os.kill(int(client.read_text()), signal.SIGTERM)
        except ProcessLookupError: pass
    (root / (name + ".running")).unlink(missing_ok=True)
    (root / (name + ".created")).unlink(missing_ok=True)
    event("removed", name)
elif args[:2] == ["container", "ls"]:
    if (root / "daemon-error").exists():
        sys.exit(1)
    name = args[-1].removeprefix("name=^/").removesuffix("$")
    if (root / (name + ".created")).exists():
        print(name)
else:
    raise SystemExit("unexpected docker call: " + repr(args))
""")
        docker.chmod(0o755)
        self.addCleanup(self.cleanup_containers)
        def stop_reader():
            import json
            os.write(self.event_fd, (json.dumps(["reader-stop", ""]) + "\n").encode())
            reader.join(timeout=30)
            self.assertFalse(reader.is_alive())
        self.addCleanup(stop_reader)

    def cleanup_containers(self):
        import signal
        self.stop_workers()
        self.workers = []
        (self.root / "hold-cleanup").unlink(missing_ok=True)
        for file in self.root.glob("process.*"):
            try:
                os.kill(int(file.name.split(".")[1]), signal.SIGTERM)
            except ProcessLookupError:
                pass
        for file in self.slots.glob("slot.*.keeper"):
            try:
                os.kill(int(file.read_text()), signal.SIGTERM)
            except (FileNotFoundError, ProcessLookupError):
                pass
        if self.slots.exists():
            subprocess.run(["flock", "-w", "30", str(self.slots / "slot.1"), "true"],
                           check=True, timeout=30)

    def container(self, name="job", **extra):
        import threading
        for suffix in ("exit", "remove"):
            os.mkfifo(self.root / (name + "." + suffix))
        env = self.env(CI_SLOT_WAIT="30", CI_SLOT_POLL="0.001",
                       PATH=str(self.bin) + ":" + os.environ["PATH"],
                       SLOT_DOCKER_ROOT=str(self.root)) | extra
        proc = subprocess.Popen(["bash", str(self.hooks / "ci-runner-slot.sh"),
                                 "--container", name, "30", "--", "docker", "create",
                                 "--name", name, "fixture"], env=env,
                                stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                stderr=subprocess.STDOUT, text=True, start_new_session=True)
        self.workers.append(proc)
        def ended():
            proc.wait()
            self.events.put(["caller-exit", name, proc.returncode])
        threading.Thread(target=ended, daemon=True).start()
        return proc

    def event(self, kind, name="job"):
        import queue
        import time
        end = time.monotonic() + 30
        while True:
            try:
                event = self.events.get(timeout=max(0, end - time.monotonic()))
            except queue.Empty:
                self.fail("missing " + kind + " event for " + name)
            if event[:2] == [kind, name]:
                return event
            if event[:2] == ["caller-exit", name] and kind in ("create", "start", "past-grant-window"):
                self.fail("caller ended before " + kind + ": " + repr(event))

    def line(self, proc):
        import queue
        import threading
        result = queue.Queue()
        threading.Thread(target=lambda: result.put(proc.stdout.readline()), daemon=True).start()
        try:
            return result.get(timeout=30)
        except queue.Empty:
            self.fail("missing process output")

    def release_container(self, suffix, name="job", text="0"):
        import threading
        def write():
            with (self.root / (name + "." + suffix)).open("w") as stream:
                stream.write(text)
        writer = threading.Thread(target=write, daemon=True)
        writer.start()
        writer.join(timeout=30)
        self.assertFalse(writer.is_alive(), "container did not accept the event")

    def assert_slot_reserved(self):
        result = subprocess.run(["flock", "-n", str(self.slots / "slot.1"), "true"],
                                timeout=30)
        self.assertEqual(result.returncode, 1)

    def test_container_and_github_jobs_share_the_same_slot(self):
        self.container_fixture()
        first, _ = self.worker()
        command = self.container()
        self.assertEqual(self.line(command).strip(), "waiting for a go-test slot")
        first.kill()
        first.wait(timeout=30)
        self.event("start")
        self.assert_slot_reserved()
        other, out = self.worker()
        self.assertIn("hook-done 1", out)
        self.release_container("exit")
        self.event("removed")
        self.assertEqual(command.wait(timeout=30), 0)
        _, out = self.worker()
        self.assertIn("hook-done 0", out)

    def test_container_caller_death_keeps_slot_until_removal(self):
        self.container_fixture()
        command = self.container()
        self.event("start")
        (self.root / "hold-cleanup").touch()
        command.kill()
        command.wait(timeout=30)
        self.event("remove")
        self.assert_slot_reserved()
        self.release_container("remove")
        self.event("removed")

    def test_docker_client_death_keeps_slot_until_removal(self):
        import signal
        self.container_fixture()
        command = self.container()
        event = self.event("start")
        (self.root / "hold-cleanup").touch()
        os.kill(event[2], signal.SIGKILL)
        self.event("remove")
        self.assertTrue((self.root / "job.running").exists())
        self.assert_slot_reserved()
        self.release_container("remove")
        self.event("removed")
        self.assertNotEqual(command.wait(timeout=30), 0)

    def test_killed_keeper_reservation_is_recovered_before_hook_grant(self):
        import signal
        self.container_fixture()
        command = self.container()
        self.event("start")
        (self.root / "hold-cleanup").touch()
        os.kill(int((self.slots / "slot.1.keeper").read_text()), signal.SIGKILL)
        self.assertEqual(command.wait(timeout=30), 75)
        self.assertTrue((self.slots / "slot.1.container").exists())
        # A new hook must recover the old daemon container, even though the
        # killed keeper no longer holds the kernel lock.
        env = self.env(CI_SLOT_POLL="0.001",
                       PATH=str(self.bin) + ":" + os.environ["PATH"],
                       SLOT_DOCKER_ROOT=str(self.root))
        proc = subprocess.Popen([str(self.root / "Runner.Worker"), "-c",
                                 f"bash {self.hooks / 'ci-runner-slot.sh'}; echo hook-done $?; read -r _"],
                                env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True, start_new_session=True)
        self.workers.append(proc)
        self.event("remove")
        self.assert_slot_reserved()
        self.release_container("remove")
        self.assertEqual(self.line(proc).strip(), "go-test slot 1 of 1 taken")
        self.assertEqual(self.line(proc).strip(), "hook-done 0")
        self.assertFalse((self.root / "job.running").exists())

    def test_container_command_refuses_unusable_slot_directory(self):
        self.container_fixture()
        bad = self.root / "not-a-directory"
        bad.touch()
        command = self.container(CI_SLOT_DIR=str(bad))
        self.assertNotEqual(command.wait(timeout=30), 0)
        self.assertFalse((self.root / "job.created").exists())

    def test_execution_deadline_stops_container_before_release(self):
        self.container_fixture()
        clock = self.root / "clock"
        clock.write_text("0")
        init = self.root / "clock-env"
        # Advance Bash's elapsed clock at command boundaries. The Docker state
        # and the flock remain real; no fixed sleep stands in for an event.
        init.write_text('set -T\ntrap \'SECONDS=$(<"$SLOT_TEST_CLOCK")\' DEBUG\n')
        command = self.container(BASH_ENV=str(init), SLOT_TEST_CLOCK=str(clock))
        self.event("start")
        (self.root / "hold-cleanup").touch()
        clock.write_text("31")
        self.event("remove")
        self.assertTrue((self.root / "job.running").exists())
        self.assert_slot_reserved()
        self.release_container("remove")
        self.event("removed")
        self.assertNotEqual(command.wait(timeout=30), 0)

    def test_ssh_hangup_stops_container_before_release(self):
        import signal
        self.container_fixture()
        command = self.container()
        self.event("start")
        (self.root / "hold-cleanup").touch()
        command.send_signal(signal.SIGHUP)
        self.event("remove")
        self.assert_slot_reserved()
        self.release_container("remove")
        self.event("removed")
        self.assertNotEqual(command.wait(timeout=30), 0)

    def test_cancelled_waiter_never_creates_a_container(self):
        self.container_fixture()
        first, _ = self.worker()
        command = self.container()
        self.assertEqual(self.line(command).strip(), "waiting for a go-test slot")
        command.terminate()
        self.assertNotEqual(command.wait(timeout=30), 0)
        self.assertFalse((self.root / "job.created").exists())
        self.assert_slot_reserved()
        first.kill()
        first.wait(timeout=30)

    def test_daemon_error_does_not_clear_container_reservation(self):
        import signal
        self.container_fixture()
        command = self.container()
        event = self.event("start")
        (self.root / "hold-cleanup").touch()
        (self.root / "daemon-error").touch()
        os.kill(event[2], signal.SIGKILL)
        self.event("remove")
        self.release_container("remove")
        # A second removal attempt proves the first failed. The keeper must
        # retain both the kernel lock and the saved container reservation.
        self.event("remove")
        self.assert_slot_reserved()
        self.assertTrue((self.slots / "slot.1.container").exists())
        (self.root / "daemon-error").unlink()
        (self.root / "hold-cleanup").unlink()
        self.release_container("remove")
        self.event("removed")
        command.wait(timeout=30)

    def test_container_exit_failure_is_preserved_after_cleanup(self):
        self.container_fixture()
        command = self.container()
        self.event("start")
        self.release_container("exit", text="7")
        self.event("removed")
        self.assertEqual(command.wait(timeout=30), 7)

    def test_hook_hold_limit_never_releases_an_active_container(self):
        self.container_fixture()
        command = self.container(CI_SLOT_HOLD="0")
        self.event("start")
        self.assert_slot_reserved()
        self.assertIsNone(command.poll())
        self.release_container("exit")
        self.event("removed")
        self.assertEqual(command.wait(timeout=30), 0)

    def test_container_holder_leaves_queue_for_other_free_slots(self):
        self.container_fixture()
        command = self.container(CI_GO_TEST_SLOTS="2")
        self.event("start")
        _, out = self.worker(CI_GO_TEST_SLOTS="2")
        self.assertIn("go-test slot 2 of 2 taken", out)
        self.release_container("exit")
        self.event("removed")
        self.assertEqual(command.wait(timeout=30), 0)

    def test_delayed_stale_cleanup_keeps_the_same_waiting_keeper(self):
        self.container_fixture()
        self.slots.mkdir()
        (self.slots / "slot.1.container").write_text("stale\n")
        (self.root / "stale.created").touch()
        (self.root / "stale.running").touch()
        (self.root / "daemon-error").touch()
        clock = self.root / "clock"
        clock.write_text("0")
        init = self.root / "clock-env"
        init.write_text(r"""set -T
if [ ! -s "$SLOT_CLOCK_PARENT" ]; then echo $$ > "$SLOT_CLOCK_PARENT"; fi
trap 'SECONDS=$(<"$SLOT_TEST_CLOCK"); if [ "$SECONDS" -ge 31 ] && [ "$$" = "$(<"$SLOT_CLOCK_PARENT")" ] && [ "$BASH_COMMAND" = "sleep 0.02" ] && [ ! -e "$SLOT_CLOCK_SEEN" ]; then : > "$SLOT_CLOCK_SEEN"; printf "[\"past-grant-window\",\"waiter\",%s]\n" "$$" > "$SLOT_EVENTS"; fi' DEBUG
""")
        command = self.container("waiter", BASH_ENV=str(init), SLOT_TEST_CLOCK=str(clock),
                                 SLOT_CLOCK_PARENT=str(self.root / "clock-parent"),
                                 SLOT_CLOCK_SEEN=str(self.root / "clock-seen"),
                                 SLOT_EVENTS=str(self.root / "events"), CI_SLOT_WAIT="60")
        self.event("remove", "stale")
        keeper = (self.slots / "slot.1.keeper").read_text()
        clock.write_text("31")
        # This event is emitted only after the take loop passes its elapsed-time
        # condition and waits again. A caller that hit the old cliff exits instead.
        self.event("past-grant-window", "waiter")
        self.assertIsNone(command.poll())
        (self.root / "daemon-error").unlink()
        self.event("start", "waiter")
        self.assertEqual((self.slots / "slot.1.keeper").read_text(), keeper)
        self.release_container("exit", "waiter")
        self.event("removed", "waiter")
        self.assertEqual(command.wait(timeout=30), 0)

    def test_dead_github_worker_cannot_receive_a_queued_grant(self):
        first, _ = self.worker()
        waiter = subprocess.Popen([str(self.root / "Runner.Worker"), "-c",
                                   f"bash {self.hooks / 'ci-runner-slot.sh'}; read -r _"],
                                  env=self.env(workspace="/work/dead", CI_SLOT_WAIT="30"),
                                  stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True,
                                  start_new_session=True)
        self.workers.append(waiter)
        self.assertEqual(self.line(waiter).strip(), "waiting for a go-test slot")
        waiter.kill()
        waiter.wait(timeout=30)
        first.kill()
        first.wait(timeout=30)
        self.assertEqual(self.line(waiter), "", "dead GitHub job received a slot grant")


if __name__ == "__main__":
    unittest.main()
