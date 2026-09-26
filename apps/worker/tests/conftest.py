"""Pytest bootstrap for the worker test suite.

Several test modules import ``ubag_worker`` (and the mock adapter package)
directly, relying on the runner having put them on PYTHONPATH. That only holds
for the ``unittest discover`` invocation the repo runner used, and only when the
whole ``tests`` directory is passed - pointing pytest at a single file, or
running it from a different rootdir, fails collection with
``ModuleNotFoundError: No module named 'ubag_worker'``.

pytest's default import mode inserts the test file's own directory (or the
nearest package root) into ``sys.path``, not the application source roots. So
add them here explicitly and the suite is importable regardless of how pytest
is invoked or what the current working directory is.
"""

import os
import sys

_TESTS_DIR = os.path.dirname(os.path.abspath(__file__))
_WORKER_ROOT = os.path.dirname(_TESTS_DIR)          # apps/worker
_REPO_ROOT = os.path.dirname(os.path.dirname(_WORKER_ROOT))  # repo root

for _path in (_WORKER_ROOT, os.path.join(_REPO_ROOT, "adapters", "mock")):
    if _path not in sys.path:
        sys.path.insert(0, _path)
