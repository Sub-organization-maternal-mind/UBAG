"""Synthetic chat fixture adapter (test only).

Runs through the worker's live engine, not through this class: see
``ubag_worker.live.synthetic`` (UBAG_SYNTHETIC_PROVIDER=1) and ``tools/synthetic-provider``.
"""


class SyntheticChatAdapter:
    """Entrypoint placeholder so the manifest resolves; direct execution is refused."""

    def run(self, payload):
        raise NotImplementedError(
            "synthetic_chat runs through the live engine (UBAG_SYNTHETIC_PROVIDER=1), not the registry dispatch"
        )
