#!/usr/bin/env python3
"""Window-isolation regressions; never capture or send input to a real window."""
import ast
import os
from pathlib import Path
import subprocess
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import patch


SOURCE = Path(__file__).with_name("gui_smoke.py")


def smoke_functions(**values):
    # Load only definitions: importing the opt-in runner would launch its app.
    tree = ast.parse(SOURCE.read_text())
    definitions: list[ast.stmt] = [node for node in tree.body if isinstance(node, ast.FunctionDef)]
    namespace = {"subprocess": subprocess, "time": __import__("time"), **values}
    exec(compile(ast.Module(body=definitions, type_ignores=[]), str(SOURCE), "exec"), namespace)
    return namespace


class WindowIsolationTests(unittest.TestCase):
    def test_search_requires_pid_and_title(self):
        calls = []
        def run(argv, **kwargs):
            calls.append(argv)
            return SimpleNamespace(stdout="", returncode=1)
        scope = smoke_functions(proc=SimpleNamespace(pid=12345))
        with patch.object(subprocess, "run", side_effect=run):
            scope["windows"]()
        self.assertIn("--all", calls[0], "xdotool defaults to OR matching")

    def test_capture_and_close_reject_wrong_pid_before_any_action(self):
        calls = []
        def run(argv, **kwargs):
            calls.append(argv)
            if argv[:2] == ["xdotool", "getwindowpid"]:
                return SimpleNamespace(stdout="99999\n", returncode=0)
            return SimpleNamespace(stdout="", returncode=0)
        scope = smoke_functions(proc=SimpleNamespace(pid=12345, poll=lambda: None), evidence=Path("unused"))
        scope["wait_for"] = lambda *args, **kwargs: ["77"]
        for action, argument in (("screenshot", "must-not-capture"), ("close_window", "77")):
            with self.subTest(action=action):
                calls.clear()
                with patch.object(subprocess, "run", side_effect=run):
                    with self.assertRaisesRegex(AssertionError, "owner|PID|pid"):
                        scope[action](argument)
                self.assertEqual(calls, [["xdotool", "getwindowpid", "77"]])

    def test_capture_rechecks_owner_after_activation(self):
        calls = []
        owners = iter(("12345", "99999"))
        def run(argv, **kwargs):
            calls.append(argv)
            owner = next(owners) if argv[:2] == ["xdotool", "getwindowpid"] else ""
            return SimpleNamespace(stdout=owner, returncode=0)
        scope = smoke_functions(proc=SimpleNamespace(pid=12345, poll=lambda: None), evidence=Path("unused"))
        scope["wait_for"] = lambda *args, **kwargs: ["77"]
        with patch.object(subprocess, "run", side_effect=run):
            with self.assertRaisesRegex(AssertionError, "PID"):
                scope["screenshot"]("must-not-capture")
        self.assertEqual(calls, [["xdotool", "getwindowpid", "77"],
                                 ["xdotool", "windowactivate", "--sync", "77"],
                                 ["xdotool", "getwindowpid", "77"]])

    @unittest.skipUnless(os.environ.get("DISPLAY"), "requires opt-in X display")
    def test_real_matching_title_decoy_is_excluded(self):
        # This GTK process owns its window and publishes its XID directly: no
        # title-only discovery, capture, activation, keyboard input or WM close.
        decoy = subprocess.Popen([sys.executable, str(SOURCE.with_name("gui_decoy.py"))],
                                 stdout=subprocess.PIPE, text=True)
        try:
            assert decoy.stdout is not None
            window = decoy.stdout.readline().strip()
            self.assertTrue(window.isdecimal(), "decoy did not publish its XID")
            scope = smoke_functions(proc=SimpleNamespace(pid=os.getpid()))
            self.assertNotIn(window, scope["windows"](), "foreign-PID title match selected")
            self.assertIsNone(decoy.poll())
        finally:
            decoy.terminate()
            decoy.wait(timeout=5)
            if decoy.stdout is not None:
                decoy.stdout.close()


if __name__ == "__main__":
    unittest.main(verbosity=2)
