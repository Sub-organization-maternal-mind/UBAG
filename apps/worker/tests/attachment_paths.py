"""Helpers for attachment-path test fixtures.

The worker now refuses any ``attachment_local_paths`` / ``audio_local_path``
entry that does not resolve inside the system temp directory - that check is
what stops a job payload from naming an arbitrary host file and having its
contents uploaded to a model provider.

These fixtures used to hardcode POSIX paths like ``/tmp/ubag/report.pdf``, which
silently depended on the suite only ever running where ``/tmp`` is the temp dir.
Build them from ``tempfile.gettempdir()`` instead so they exercise the same
check and stay correct on every platform.

The files are not created on disk: the driver under test is a fake that only
records the paths it is handed.
"""

import os
import tempfile


def temp_attachment(name: str) -> str:
    """An absolute path inside the system temp dir, shaped like a gateway-
    materialized attachment (executor/workerconsumer.go writes these under
    os.MkdirTemp("", "ubag-attach-*"))."""
    return os.path.join(tempfile.gettempdir(), "ubag-attach-fixture", name)
