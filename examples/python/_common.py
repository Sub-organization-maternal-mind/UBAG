"""Shared setup for the Python examples.

Install the client first (stdlib only):  pip install -e packages/sdk-python
Environment: UBAG_TOKEN (app secret / PAT, keep it server-side) and optionally
UBAG_BASE_URL (default http://127.0.0.1:8080).
"""
import os
import struct
import sys

from ubag_client import UbagClient


def client_from_env() -> UbagClient:
    token = os.environ.get("UBAG_TOKEN")
    if not token:
        sys.exit("Set UBAG_TOKEN (and optionally UBAG_BASE_URL).")
    return UbagClient(os.environ.get("UBAG_BASE_URL", "http://127.0.0.1:8080"), token)


def read_or(path, fallback):
    """File bytes when a path is given, otherwise a generated placeholder."""
    if path:
        with open(path, "rb") as handle:
            return handle.read()
    return fallback()


def tiny_png() -> bytes:
    import base64

    return base64.b64decode(
        "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
    )


def silent_wav(seconds: float = 0.5, rate: int = 16000) -> bytes:
    samples = int(seconds * rate)
    data = b"\x00\x00" * samples
    header = b"RIFF" + struct.pack("<I", 36 + len(data)) + b"WAVEfmt " + struct.pack("<IHHIIHH", 16, 1, 1, rate, rate * 2, 2, 16)
    return header + b"data" + struct.pack("<I", len(data)) + data
