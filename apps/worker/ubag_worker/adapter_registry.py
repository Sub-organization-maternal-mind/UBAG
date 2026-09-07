"""Adapter manifest loading and safe-mode dispatch for the UBAG worker."""

from __future__ import annotations

import importlib
import json
import os
import sys
from pathlib import Path
from typing import Any, Dict, List, Mapping

from .live.events import digest

# Shared safe-mode scanner (leaf module, no import cycle).
from .live.secret_scan import (  # noqa: F401 - re-exported for historical callers
    _BEARER_VALUE_PATTERN,
    _CAPTCHA_SOLVER_PATTERN,
    _DISALLOWED_COMPACT_SECRET_MARKERS,
    _DISALLOWED_SECRET_KEYS,
    _DISALLOWED_SECRET_SEGMENTS,
    _PRIVATE_KEY_VALUE_PATTERN,
    _contains_disallowed_secret_material,
    _is_disallowed_secret_key,
    _is_secret_reference_key,
    _normalize_secret_key,
)

JsonObject = Dict[str, Any]

_REPO_ROOT = Path(__file__).resolve().parents[3]
_ADAPTERS_ROOT = _REPO_ROOT / "adapters"
_REGISTRY_PATH = _ADAPTERS_ROOT / "registry.json"

REQUIRED_ADAPTER_IDS = (
    "mock",
    "generic_chat",
    "generic_form",
    "deepseek_web",
    "chatgpt_web",
    "claude_web",
    "gemini_web",
    "mistral_lechat",
    "perplexity_web",
    "duckai_web",
)

_FORBIDDEN_SAFE_MODE_FIELDS = (
    "automated_login",
    "credential_scraping",
    "credential_storage",
    "captcha_solving",
    "captcha_bypass",
)

_REQUIRED_MANUAL_CONTEXT_FIELDS = (
    "tenant_id",
    "account_binding_id",
    "consent_ref",
    "automation_scope",
)


class AdapterRegistryError(ValueError):
    """Raised when adapter registry or manifest data is invalid."""


def load_registry(registry_path: Path = _REGISTRY_PATH) -> JsonObject:
    registry = _read_json_object(registry_path)
    if registry.get("schema_version") != "ubag.adapters.v0":
        raise AdapterRegistryError("adapter registry schema_version must be ubag.adapters.v0")
    if registry.get("kind") != "mock_registry":
        raise AdapterRegistryError("adapter registry kind must be mock_registry")
    adapters = registry.get("adapters")
    if not isinstance(adapters, list) or not adapters:
        raise AdapterRegistryError("adapter registry must contain a non-empty adapters list")
    return registry


def load_manifest(manifest_path: Path) -> JsonObject:
    manifest = _read_json_object(manifest_path)
    validate_manifest(manifest, manifest_path)
    return manifest


def load_manifests(registry_path: Path = _REGISTRY_PATH) -> Dict[str, JsonObject]:
    registry = load_registry(registry_path)
    manifests: Dict[str, JsonObject] = {}

    for entry in registry["adapters"]:
        if not isinstance(entry, Mapping):
            raise AdapterRegistryError("adapter registry entries must be JSON objects")
        adapter_id = _required_text(entry, "id", "registry entry")
        manifest_rel = _required_text(entry, "manifest", "registry entry %s" % adapter_id)
        manifest_path = _safe_adapter_path(manifest_rel)
        manifest = load_manifest(manifest_path)
        if manifest["id"] != adapter_id:
            raise AdapterRegistryError(
                "adapter registry id %s does not match manifest id %s"
                % (adapter_id, manifest["id"])
            )
        if adapter_id in manifests:
            raise AdapterRegistryError("duplicate adapter id %s" % adapter_id)
        manifests[adapter_id] = manifest

    missing = sorted(set(REQUIRED_ADAPTER_IDS) - set(manifests))
    if missing:
        raise AdapterRegistryError("adapter registry missing required adapters: %s" % ", ".join(missing))

    return manifests


def load_manifest_index(registry_path: Path = _REGISTRY_PATH) -> Dict[str, JsonObject]:
    manifests = load_manifests(registry_path)
    index: Dict[str, JsonObject] = {}
    for manifest in manifests.values():
        keys = [manifest["id"]]
        aliases = manifest.get("aliases", [])
        if not isinstance(aliases, list):
            raise AdapterRegistryError("adapter %s aliases must be a list" % manifest["id"])
        keys.extend(str(alias) for alias in aliases)
        for key in keys:
            normalized = _normalize_target_key(key)
            existing = index.get(normalized)
            if existing is not None:
                if existing["id"] == manifest["id"]:
                    continue
                raise AdapterRegistryError("duplicate adapter target or alias %s" % key)
            index[normalized] = manifest
    return index


def validate_manifest(manifest: Mapping[str, Any], manifest_path: Path) -> None:
    if not isinstance(manifest, Mapping):
        raise AdapterRegistryError("adapter manifest must be a JSON object")
    if manifest.get("schema_version") != "ubag.adapter.v0":
        raise AdapterRegistryError("adapter manifest schema_version must be ubag.adapter.v0")

    manifest_id = _required_text(manifest, "id", "adapter manifest")
    _required_text(manifest, "display_name", "adapter %s" % manifest_id)
    _required_text(manifest, "version", "adapter %s" % manifest_id)
    status = _required_text(manifest, "status", "adapter %s" % manifest_id)
    if status not in ("mock", "stub"):
        raise AdapterRegistryError("adapter %s status must be mock or stub" % manifest_id)
    _required_text(manifest, "entrypoint", "adapter %s" % manifest_id)
    adapter_path = _required_text(manifest, "adapter_path", "adapter %s" % manifest_id)

    expected_manifest_path = _safe_adapter_path("%s/manifest.json" % adapter_path)
    if manifest_path.resolve() != expected_manifest_path.resolve():
        raise AdapterRegistryError("adapter %s manifest path does not match adapter_path" % manifest_id)

    for list_field in ("supported_command_types", "capabilities", "synthetic_test_prompts"):
        if not isinstance(manifest.get(list_field), list) or not manifest[list_field]:
            raise AdapterRegistryError("adapter %s %s must be a non-empty list" % (manifest_id, list_field))

    artifact_policy = _required_mapping(manifest, "artifact_policy", "adapter %s" % manifest_id)
    if status == "mock":
        _require_policy_value(artifact_policy, manifest_id, "screenshots", "disabled")
        _require_policy_value(artifact_policy, manifest_id, "dom_snapshots", "disabled")
        _require_policy_value(artifact_policy, manifest_id, "recordings", "disabled")
    else:
        _require_policy_value(artifact_policy, manifest_id, "screenshots", "on_failure_only")
        _require_policy_value(artifact_policy, manifest_id, "dom_snapshots", "drift_baseline_only")
        _require_policy_value(artifact_policy, manifest_id, "recordings", "post_login_debug_only")

    safe_mode = _required_mapping(manifest, "safe_mode", "adapter %s" % manifest_id)
    if safe_mode.get("user_owned_sessions_only") is not True:
        raise AdapterRegistryError("adapter %s must require user-owned sessions only" % manifest_id)
    if status == "stub" and safe_mode.get("manual_login_required") is not True:
        raise AdapterRegistryError("stub adapter %s must require manual login" % manifest_id)
    for field in _FORBIDDEN_SAFE_MODE_FIELDS:
        if safe_mode.get(field) != "forbidden":
            raise AdapterRegistryError("adapter %s safe_mode.%s must be forbidden" % (manifest_id, field))

    credential_policy = _required_mapping(manifest, "credential_policy", "adapter %s" % manifest_id)
    for field in (
        "collect_credentials",
        "read_credentials_from_payload",
        "scrape_credentials",
        "store_credentials",
    ):
        if credential_policy.get(field) is not False:
            raise AdapterRegistryError("adapter %s credential_policy.%s must be false" % (manifest_id, field))

    captcha_policy = _required_mapping(manifest, "captcha_policy", "adapter %s" % manifest_id)
    for field in ("solve", "bypass", "delegate_to_solver"):
        if captcha_policy.get(field) is not False:
            raise AdapterRegistryError("adapter %s captcha_policy.%s must be false" % (manifest_id, field))
    if captcha_policy.get("manual_only") is not True:
        raise AdapterRegistryError("adapter %s captcha_policy.manual_only must be true" % manifest_id)


def resolve_manifest_for_payload(
    payload: Mapping[str, Any],
    registry_path: Path = _REGISTRY_PATH,
) -> JsonObject:
    target = _target_from_payload(payload)
    index = load_manifest_index(registry_path)
    manifest = index.get(_normalize_target_key(target))
    if manifest is None:
        raise NotImplementedError("no adapter manifest registered for target %s" % target)
    return manifest


def instantiate_adapter(manifest: Mapping[str, Any]) -> Any:
    adapter_path = _safe_adapter_path(_required_text(manifest, "adapter_path", "adapter manifest"))
    adapter_dir = adapter_path if adapter_path.is_dir() else adapter_path.parent
    adapter_dir_text = str(adapter_dir)
    if adapter_dir_text not in sys.path:
        sys.path.insert(0, adapter_dir_text)

    entrypoint = _required_text(manifest, "entrypoint", "adapter %s" % manifest.get("id", "unknown"))
    module_name, separator, attr_name = entrypoint.partition(":")
    if not separator or not module_name or not attr_name:
        raise AdapterRegistryError("adapter %s entrypoint must be module:attribute" % manifest["id"])
    module = importlib.import_module(module_name)
    adapter_type = getattr(module, attr_name)
    return adapter_type()


def events_for_payload(payload: Mapping[str, Any]) -> List[JsonObject]:
    if not isinstance(payload, Mapping):
        raise ValueError("job payload must be a JSON object")

    manifest = resolve_manifest_for_payload(payload)
    if _contains_disallowed_secret_material(payload):
        raise ValueError(
            "adapter %s payload must not include credentials, cookies, tokens, or secrets; "
            "use a user-owned manual browser session instead" % manifest["id"]
        )
    _validate_manual_session_context(manifest, payload)
    if manifest.get("status") == "stub":
        return _manual_session_required_events(manifest, payload)

    adapter = instantiate_adapter(manifest)
    if hasattr(adapter, "run"):
        return list(adapter.run(payload))
    if hasattr(adapter, "iter_events"):
        return list(adapter.iter_events(payload))
    raise AdapterRegistryError("adapter %s has no run or iter_events hook" % manifest["id"])


def _target_from_payload(payload: Mapping[str, Any]) -> str:
    job_payload = payload.get("job", {})
    if job_payload is None:
        job_payload = {}
    if not isinstance(job_payload, Mapping):
        raise ValueError("payload.job must be a JSON object when present")

    target = job_payload.get("target", payload.get("target", "mock"))
    if target is None:
        return "mock"
    target_text = str(target).strip()
    return target_text if target_text else "mock"


def _validate_manual_session_context(manifest: Mapping[str, Any], payload: Mapping[str, Any]) -> None:
    if manifest.get("status") != "stub":
        return

    from .live.envelope import manual_context as _manual_context

    context = _manual_context(payload)
    if _single_user_edge_enabled():
        return

    missing = []
    for field in _REQUIRED_MANUAL_CONTEXT_FIELDS:
        value = context.get(field)
        if field == "automation_scope":
            if not isinstance(value, list) or not value:
                missing.append(field)
        elif not isinstance(value, str) or not value.strip():
            missing.append(field)

    if missing:
        raise ValueError(
            "adapter %s requires user-owned manual session context fields: %s"
            % (manifest["id"], ", ".join(missing))
        )


def _manual_session_required_events(manifest: Mapping[str, Any], payload: Mapping[str, Any]) -> List[JsonObject]:
    # Shared normalization from live.envelope (lazy: envelope imports this
    # module's secret scanner at module level, so we cannot import it eagerly).
    from .live.envelope import (
        _derive_job_id,
        _safe_session_id,
        _worker_event,
    )
    from .live.envelope import (
        manual_context as _manual_context,
    )

    job_payload = payload.get("job", {})
    if not isinstance(job_payload, Mapping):
        job_payload = {}
    context = _manual_context(payload)
    api_version = str(payload.get("api_version", "2026-05-22"))
    job_id = _derive_job_id(payload, job_payload)
    trace_id = str(payload.get("trace_id", "trace_" + digest(job_id)[:16]))
    target = str(job_payload.get("target", manifest["id"]))
    session_id = _safe_session_id(context.get("session_id"), job_id, target)
    novnc_url = "http://127.0.0.1:7900/session/%s" % session_id
    manual_context = _effective_manual_context(context)

    return [
        _worker_event(api_version, job_id, trace_id, 1, "queued", {
            "status": "queued",
            "target": target,
            "adapter": manifest["id"],
            "message": "job accepted by safe manual-session worker",
        }),
        _worker_event(api_version, job_id, trace_id, 2, "session.manual_action_required", {
            "status": "manual_action_required",
            "target": target,
            "adapter": manifest["id"],
            "session_id": session_id,
            "novnc_url": novnc_url,
            "account_binding_id": manual_context["account_binding_id"],
            "consent_ref": manual_context["consent_ref"],
            "automation_scope": manual_context["automation_scope"],
            "reason": "manual_login_required",
            "message": "Open the live browser session and complete login, CAPTCHA, 2FA, or consent prompts manually.",
        }),
        _worker_event(api_version, job_id, trace_id, 3, "blocked", {
            "status": "blocked",
            "target": target,
            "adapter": manifest["id"],
            "reason": "manual_browser_runtime_required",
            "retryable": True,
            "message": "Provider execution is paused until a user-owned live browser session is attached.",
        }),
    ]


def _single_user_edge_enabled() -> bool:
    return os.environ.get("UBAG_WORKER_SINGLE_USER_EDGE", "").strip().lower() in ("1", "true", "yes")


def _effective_manual_context(context: Mapping[str, Any]) -> JsonObject:
    if not _single_user_edge_enabled():
        return {
            "account_binding_id": str(context["account_binding_id"]).strip(),
            "consent_ref": str(context["consent_ref"]).strip(),
            "automation_scope": list(context["automation_scope"]),
        }

    account_binding_id = context.get("account_binding_id")
    consent_ref = context.get("consent_ref")
    automation_scope = context.get("automation_scope")

    return {
        "account_binding_id": (
            account_binding_id.strip()
            if isinstance(account_binding_id, str) and account_binding_id.strip()
            else "single_user_edge"
        ),
        "consent_ref": (
            consent_ref.strip()
            if isinstance(consent_ref, str) and consent_ref.strip()
            else "single_user_edge_local_consent"
        ),
        "automation_scope": (
            list(automation_scope)
            if isinstance(automation_scope, list) and automation_scope
            else ["manual_login", "submit_prompt", "read_response"]
        ),
    }


def _require_policy_value(policy: Mapping[str, Any], adapter_id: str, field: str, expected: str) -> None:
    if policy.get(field) != expected:
        raise AdapterRegistryError(
            "adapter %s artifact_policy.%s must be %s" % (adapter_id, field, expected)
        )


def _safe_adapter_path(relative_path: str) -> Path:
    candidate = (_ADAPTERS_ROOT / relative_path).resolve()
    adapters_root = _ADAPTERS_ROOT.resolve()
    try:
        candidate.relative_to(adapters_root)
    except ValueError:
        raise AdapterRegistryError("adapter path escapes adapters root: %s" % relative_path)
    return candidate


def _read_json_object(path: Path) -> JsonObject:
    try:
        data = json.loads(path.read_text(encoding="utf-8-sig"))
    except FileNotFoundError:
        raise AdapterRegistryError("missing JSON file %s" % path)
    if not isinstance(data, dict):
        raise AdapterRegistryError("JSON file %s must contain an object" % path)
    return data


def _required_mapping(value: Mapping[str, Any], field: str, context: str) -> Mapping[str, Any]:
    child = value.get(field)
    if not isinstance(child, Mapping):
        raise AdapterRegistryError("%s %s must be a JSON object" % (context, field))
    return child


def _required_text(value: Mapping[str, Any], field: str, context: str) -> str:
    child = value.get(field)
    if not isinstance(child, str) or not child.strip():
        raise AdapterRegistryError("%s %s must be a non-empty string" % (context, field))
    return child.strip()


def _normalize_target_key(value: str) -> str:
    return value.strip().lower().replace("-", "_")


__all__ = [
    "AdapterRegistryError",
    "REQUIRED_ADAPTER_IDS",
    "events_for_payload",
    "instantiate_adapter",
    "load_manifest",
    "load_manifest_index",
    "load_manifests",
    "load_registry",
    "resolve_manifest_for_payload",
    "validate_manifest",
]
