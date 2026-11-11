import os
import fcntl
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest

import target_bootstrap_old_start_guard as guard


class TargetOldStartGuardTest(unittest.TestCase):
    def test_absent_fence_allows_then_any_entry_denies(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            directory = root / 'controlled'
            directory.mkdir(mode=0o700)
            (directory / guard.LOCK).touch(mode=0o444)
            self.assertTrue(guard.may_start(directory, root, os.getuid()))
            fence = directory / guard.FENCE
            fence.write_text('not necessarily valid json')
            self.assertFalse(guard.may_start(directory, root, os.getuid()))
            fence.unlink()
            fence.symlink_to('/nonexistent')
            self.assertFalse(guard.may_start(directory, root, os.getuid()))

    def test_untrusted_or_missing_directory_denies(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            directory = root / 'controlled'
            directory.mkdir(mode=0o700)
            (directory / guard.LOCK).touch(mode=0o444)
            self.assertFalse(guard.may_start(directory, root, os.getuid() + 1))
            directory.chmod(0o777)
            self.assertFalse(guard.may_start(directory, root, os.getuid()))
            self.assertFalse(guard.may_start(root / 'missing', root, os.getuid()))

    def test_service_user_must_be_able_to_read_the_lock(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            directory = root / 'controlled'
            directory.mkdir(mode=0o700)
            lock = directory / guard.LOCK
            lock.touch(mode=0o600)
            self.assertFalse(guard.may_start(directory, root, os.getuid()))

    def test_start_waits_for_fence_writer_then_denies(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            directory = root / 'controlled'
            directory.mkdir(mode=0o700)
            (directory / guard.LOCK).touch(mode=0o444)
            with (directory / guard.LOCK).open('rb') as lock:
                fcntl.flock(lock, fcntl.LOCK_EX)
                child = subprocess.Popen([sys.executable, '-c',
                    'import os,sys; from pathlib import Path; import target_bootstrap_old_start_guard as g; '
                    'print(g.may_start(Path(sys.argv[1]),Path(sys.argv[2]),os.getuid()),flush=True)',
                    str(directory), str(root)], cwd=Path(__file__).parent,
                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
                try:
                    time.sleep(.2)
                    self.assertIsNone(child.poll(), 'start was not fenced by the exclusive lock')
                    (directory / guard.FENCE).write_text('fenced')
                finally:
                    fcntl.flock(lock, fcntl.LOCK_UN)
                output, _ = child.communicate(timeout=5)
                self.assertEqual(output.strip(), 'False')
                self.assertEqual(child.returncode, 0)

    def test_start_lock_is_held_after_exec(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            directory = root / 'controlled'
            directory.mkdir(mode=0o700)
            (directory / guard.LOCK).touch(mode=0o444)
            child = subprocess.Popen([sys.executable, '-c',
                'import os,sys; from pathlib import Path; import target_bootstrap_old_start_guard as g; '
                'fd=g.acquire_start_lock(Path(sys.argv[1]),Path(sys.argv[2]),os.getuid()); '
                'assert fd is not None; print("locked",flush=True); os.execv("/bin/sleep",["sleep","5"])',
                str(directory), str(root)], cwd=Path(__file__).parent,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                self.assertEqual(child.stdout.readline().strip(), 'locked')
                with (directory / guard.LOCK).open('rb') as lock:
                    with self.assertRaises(BlockingIOError):
                        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            finally:
                child.terminate()
                child.communicate(timeout=5)

    def test_start_lock_is_held_by_go_service_after_exec(self):
        with tempfile.TemporaryDirectory() as td:
            root = Path(td)
            directory = root / 'controlled'
            directory.mkdir(mode=0o700)
            (directory / guard.LOCK).touch(mode=0o444)
            source, binary = root / 'main.go', root / 'fixture-service'
            source.write_text('package main\nimport "time"\nfunc main(){time.Sleep(5*time.Second)}\n')
            env = dict(os.environ, GOWORK='off', GOENV='off', GOFLAGS='')
            subprocess.run(['go', 'build', '-o', str(binary), str(source)], check=True, env=env,
                           stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, timeout=60)
            child = subprocess.Popen([sys.executable, '-c',
                'import os,sys; from pathlib import Path; import target_bootstrap_old_start_guard as g; '
                'fd=g.acquire_start_lock(Path(sys.argv[1]),Path(sys.argv[2]),os.getuid()); '
                'assert fd is not None; print("locked",flush=True); os.execv("/usr/bin/env",["env","FIXTURE=1",sys.argv[3]])',
                str(directory), str(root), str(binary)], cwd=Path(__file__).parent,
                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            try:
                self.assertEqual(child.stdout.readline().strip(), 'locked')
                time.sleep(.15)
                self.assertIsNone(child.poll())
                with (directory / guard.LOCK).open('rb') as lock:
                    with self.assertRaises(BlockingIOError):
                        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            finally:
                child.terminate()
                child.communicate(timeout=5)


if __name__ == '__main__':
    unittest.main()
