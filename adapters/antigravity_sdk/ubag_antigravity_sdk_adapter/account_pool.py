"""Multi-account pool for Antigravity SDK adapter.

Manages 2-3 Antigravity accounts with round-robin or least-quota-used
selection, per-account API key configuration, and health tracking.
"""

from __future__ import annotations

import os
import threading
import time
from dataclasses import dataclass
from typing import Any, Dict, List, Optional


@dataclass
class AccountConfig:
    """Configuration for a single Antigravity account."""

    account_id: str
    label: str
    api_key: str
    tier: str = "free"
    enabled: bool = True
    last_used: float = 0.0
    use_count: int = 0
    cooldown_until: float = 0.0

    @property
    def is_available(self) -> bool:
        if not self.enabled:
            return False
        return time.monotonic() >= self.cooldown_until


@dataclass
class PoolStats:
    """Statistics for the account pool."""

    total_accounts: int = 0
    enabled_accounts: int = 0
    exhausted_accounts: int = 0
    cooldown_accounts: int = 0
    total_uses: int = 0


class AccountPool:
    """Thread-safe pool of Antigravity accounts with rotation."""

    def __init__(
        self,
        accounts: Optional[List[AccountConfig]] = None,
        strategy: str = "round_robin",
    ) -> None:
        self._accounts: List[AccountConfig] = accounts or []
        self._strategy = strategy
        self._lock = threading.Lock()
        self._round_robin_index = 0

    @classmethod
    def from_env(cls, prefix: str = "UBAG_ANTIGRAVITY") -> "AccountPool":
        """Build account pool from environment variables.

        Reads:
        - {prefix}_ACCOUNT_COUNT (default 1)
        - {prefix}_ACCOUNT_{n}_LABEL
        - {prefix}_ACCOUNT_{n}_API_KEY
        - {prefix}_ACCOUNT_{n}_TIER (optional, default "free")
        - {prefix}_ACCOUNT_{n}_ENABLED (optional, default true)
        """
        count_str = os.environ.get(f"{prefix}_ACCOUNT_COUNT", "1")
        try:
            count = int(count_str)
        except (TypeError, ValueError):
            count = 1

        accounts: List[AccountConfig] = []
        for i in range(1, count + 1):
            api_key = os.environ.get(f"{prefix}_ACCOUNT_{i}_API_KEY", "")
            if not api_key:
                continue

            label = os.environ.get(f"{prefix}_ACCOUNT_{i}_LABEL", f"Account {i}")
            tier = os.environ.get(f"{prefix}_ACCOUNT_{i}_TIER", "free")
            enabled_str = os.environ.get(f"{prefix}_ACCOUNT_{i}_ENABLED", "true")
            enabled = enabled_str.strip().lower() not in ("0", "false", "no", "off")

            accounts.append(
                AccountConfig(
                    account_id=f"acct_{i}",
                    label=label,
                    api_key=api_key,
                    tier=tier,
                    enabled=enabled,
                )
            )

        return cls(accounts=accounts)

    @property
    def accounts(self) -> List[AccountConfig]:
        return list(self._accounts)

    def add_account(self, config: AccountConfig) -> None:
        with self._lock:
            for i, acct in enumerate(self._accounts):
                if acct.account_id == config.account_id:
                    self._accounts[i] = config
                    return
            self._accounts.append(config)

    def remove_account(self, account_id: str) -> bool:
        with self._lock:
            for i, acct in enumerate(self._accounts):
                if acct.account_id == account_id:
                    self._accounts.pop(i)
                    return True
            return False

    def update_account(self, account_id: str, **kwargs: Any) -> bool:
        with self._lock:
            for acct in self._accounts:
                if acct.account_id == account_id:
                    for key, value in kwargs.items():
                        if hasattr(acct, key):
                            setattr(acct, key, value)
                    return True
            return False

    def acquire(self) -> Optional[AccountConfig]:
        """Select the next available account based on strategy."""
        with self._lock:
            available = [a for a in self._accounts if a.is_available]
            if not available:
                return None

            if self._strategy == "least_used":
                selected = min(available, key=lambda a: a.use_count)
            else:
                selected = available[self._round_robin_index % len(available)]
                self._round_robin_index += 1

            selected.last_used = time.monotonic()
            selected.use_count += 1
            return selected

    def release(self, account_id: str, *, cooldown_seconds: float = 0.0) -> None:
        """Release an account back to the pool, optionally with cooldown."""
        with self._lock:
            for acct in self._accounts:
                if acct.account_id == account_id:
                    if cooldown_seconds > 0:
                        acct.cooldown_until = time.monotonic() + cooldown_seconds
                    return

    def mark_exhausted(self, account_id: str, cooldown_seconds: float = 300.0) -> None:
        """Mark an account as exhausted (e.g., quota depleted)."""
        self.release(account_id, cooldown_seconds=cooldown_seconds)

    def get_account(self, account_id: str) -> Optional[AccountConfig]:
        for acct in self._accounts:
            if acct.account_id == account_id:
                return acct
        return None

    def stats(self) -> PoolStats:
        with self._lock:
            available = [a for a in self._accounts if a.is_available]
            return PoolStats(
                total_accounts=len(self._accounts),
                enabled_accounts=sum(1 for a in self._accounts if a.enabled),
                exhausted_accounts=sum(1 for a in self._accounts if not a.enabled),
                cooldown_accounts=len(self._accounts) - len(available),
                total_uses=sum(a.use_count for a in self._accounts),
            )

    def to_dict_list(self) -> List[Dict[str, Any]]:
        """Serialize account configs for API responses (without API keys)."""
        result = []
        for acct in self._accounts:
            result.append(
                {
                    "account_id": acct.account_id,
                    "label": acct.label,
                    "tier": acct.tier,
                    "enabled": acct.enabled,
                    "last_used": acct.last_used,
                    "use_count": acct.use_count,
                    "is_available": acct.is_available,
                    "cooldown_until": acct.cooldown_until,
                }
            )
        return result
