from __future__ import annotations

import json
import sys
import unittest
from pathlib import Path
from typing import Any, Dict, Mapping, Optional, Tuple
from urllib.parse import parse_qs, unquote, urlsplit

PACKAGE_ROOT = Path(__file__).resolve().parents[1]
REPO_PACKAGES_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(PACKAGE_ROOT / "src"))

from ubag import (  # noqa: E402
    UBAG_SDK_NAME,
    UBAG_SDK_VERSION,
    UbagApiError,
    UbagClient,
    UbagResponse,
)

FIXTURE_PATH = REPO_PACKAGES_ROOT / "conformance" / "fixtures" / "v0" / "scenarios.json"
BASE_URL = "https://fixture.ubag.local"


class FixtureTransport:
    def __init__(self, scenario: Mapping[str, Any]) -> None:
        self.scenario = scenario
        self.last_request: Optional[Dict[str, Any]] = None

    def __call__(
        self,
        method: str,
        url: str,
        headers: Mapping[str, str],
        body: Optional[bytes],
        timeout: Optional[float],
    ) -> UbagResponse:
        self.last_request = {
            "method": method,
            "url": url,
            "headers": dict(headers),
            "body": body,
            "timeout": timeout,
        }
        response = self.scenario["response"]
        response_body = response.get("body")
        response_text = response.get("body_text")
        return UbagResponse(
            status=response["status"],
            reason="fixture",
            headers=response.get("headers", {}),
            body=(
                response_text.encode("utf-8")
                if isinstance(response_text, str)
                else b"" if response_body is None
                else json.dumps(response_body).encode("utf-8")
            ),
        )


class PythonSdkConformanceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.fixture = json.loads(FIXTURE_PATH.read_text(encoding="utf-8"))

    def test_shared_conformance_fixtures(self) -> None:
        self.assertEqual(self.fixture["suite"], "ubag.v0.sdk.baseline")

        for scenario in self.fixture["scenarios"]:
            with self.subTest(scenario=scenario["id"]):
                transport = FixtureTransport(scenario)
                client = self._client_for_scenario(scenario, transport)

                expected_throw = scenario["expect"].get("throws")
                if expected_throw:
                    with self.assertRaises(UbagApiError) as caught:
                        self._invoke(client, scenario)
                    self.assertEqual(caught.exception.name, expected_throw)
                    self._assert_error_expectations(caught.exception, scenario["expect"])
                else:
                    result = self._invoke(client, scenario)
                    self._assert_body_expectations(result, scenario["expect"])

                self._assert_recorded_request(scenario, transport.last_request)

    def test_create_job_adds_python_sdk_metadata_when_missing(self) -> None:
        scenario = next(
            item for item in self.fixture["scenarios"] if item["id"] == "jobs.create.accepted"
        )
        transport = FixtureTransport(scenario)
        client = UbagClient(BASE_URL, app_secret="app_secret_fixture", transport=transport)

        client.create_job(
            {
                "client": {"app_id": "fixture-app", "app_version": "0.0.0"},
                "job": {
                    "target": "mock_target",
                    "command_type": "echo",
                    "input": {"prompt": "Hello UBAG"},
                },
            },
            idempotency_key="idem_python_sdk",
        )

        assert transport.last_request is not None
        body = json.loads(transport.last_request["body"].decode("utf-8"))
        self.assertEqual(body["client"]["sdk"]["name"], UBAG_SDK_NAME)
        self.assertEqual(body["client"]["sdk"]["version"], UBAG_SDK_VERSION)
        self.assertEqual(body["idempotency_key"], "idem_python_sdk")
        self.assertEqual(
            transport.last_request["headers"]["Idempotency-Key"],
            "idem_python_sdk",
        )

    def _client_for_scenario(
        self,
        scenario: Mapping[str, Any],
        transport: FixtureTransport,
    ) -> UbagClient:
        authorization = scenario["request"].get("headers", {}).get("Authorization")
        app_secret = None
        if isinstance(authorization, str) and authorization.startswith("Bearer "):
            app_secret = authorization.removeprefix("Bearer ")
        return UbagClient(BASE_URL, app_secret=app_secret, transport=transport)

    def _invoke(self, client: UbagClient, scenario: Mapping[str, Any]) -> Any:
        request = scenario["request"]
        method = request["method"]
        path = request["path"]
        headers = request.get("headers", {})
        api_version = headers.get("Ubag-Api-Version")
        idempotency_key = headers.get("Idempotency-Key")
        split = urlsplit(path)
        route = split.path

        options: Dict[str, Any] = {}
        if api_version is not None:
            options["api_version"] = api_version
        if idempotency_key is not None:
            options["idempotency_key"] = idempotency_key

        if method == "GET" and route == "/v1/health":
            return client.health(**options)
        if method == "GET" and route == "/v1/ready":
            return client.ready(**options)
        if method == "GET" and route == "/v1/version":
            return client.version()
        if method == "GET" and route == "/v1/workflows":
            return client.list_workflows(**options)
        if method == "GET" and route == "/v1/templates":
            return client.list_templates(**options)
        if method == "GET" and route == "/v1/targets":
            params = parse_qs(split.query)
            return client.list_targets(cursor=_single(params, "cursor"), limit=_int_or_none(_single(params, "limit")), **options)
        if method == "GET" and route == "/v1/adapters":
            params = parse_qs(split.query)
            return client.list_adapters(cursor=_single(params, "cursor"), limit=_int_or_none(_single(params, "limit")), **options)
        if method == "GET" and route == "/v1/apps":
            params = parse_qs(split.query)
            return client.list_apps(cursor=_single(params, "cursor"), limit=_int_or_none(_single(params, "limit")), **options)
        if method == "GET" and route == "/v1/devices":
            params = parse_qs(split.query)
            return client.list_devices(cursor=_single(params, "cursor"), limit=_int_or_none(_single(params, "limit")), **options)
        if method == "GET" and route == "/v1/audit":
            params = parse_qs(split.query)
            return client.list_audit_events(cursor=_single(params, "cursor"), limit=_int_or_none(_single(params, "limit")), **options)
        if method == "GET" and route == "/v1/webhooks":
            params = parse_qs(split.query)
            return client.list_webhooks(cursor=_single(params, "cursor"), limit=_int_or_none(_single(params, "limit")), **options)
        if method == "GET" and route == "/v1/events":
            params = parse_qs(split.query)
            return client.list_events(cursor=_single(params, "cursor"), limit=_int_or_none(_single(params, "limit")), **options)
        if method == "GET" and route == "/v1/cache":
            return client.cache_status(**options)
        if method == "GET" and route == "/v1/metrics":
            return {"body": client.get_metrics(**options)}
        if method == "GET" and route == "/v1/jobs":
            params = parse_qs(split.query)
            return client.list_jobs(
                cursor=_single(params, "cursor"),
                limit=_int_or_none(_single(params, "limit")),
                status=_single(params, "filter[status]"),
                target=_single(params, "filter[target]"),
                sort=_single(params, "sort"),
                **options,
            )
        if method == "GET" and route.startswith("/v1/jobs/") and route.endswith("/events"):
            params = parse_qs(split.query)
            job_id = route.removeprefix("/v1/jobs/").removesuffix("/events")
            return client.list_job_events(
                job_id,
                cursor=_single(params, "cursor"),
                after_sequence=_int_or_none(_single(params, "after_sequence")),
                limit=_int_or_none(_single(params, "limit")),
                **options,
            )
        if method == "GET" and route.startswith("/v1/jobs/") and route.endswith("/artifacts"):
            job_id = route.removeprefix("/v1/jobs/").removesuffix("/artifacts")
            return client.list_job_artifacts(job_id, **options)
        if method == "GET" and route.startswith("/v1/sse/jobs/"):
            response = client.stream_job_events_sse(route.removeprefix("/v1/sse/jobs/"), **options)
            return {"body": response.body.decode("utf-8")}
        if method == "GET" and route.startswith("/v1/jobs/") and "/artifacts/" in route:
            job_id, key = _split_artifact_route(route)
            response = client.get_job_artifact(job_id, key, **options)
            headers = {key.lower(): value for key, value in response.headers.items()}
            return {
                "body": response.body.decode("utf-8"),
                "content_type": headers.get("content-type"),
                "checksum": headers.get("ubag-artifact-checksum"),
            }
        if method == "PUT" and route.startswith("/v1/jobs/") and "/artifacts/" in route:
            job_id, key = _split_artifact_route(route)
            return client.put_job_artifact(
                job_id,
                key,
                request.get("body_text", "").encode("utf-8"),
                content_type=headers.get("Content-Type", "application/octet-stream"),
                **options,
            )
        if method == "DELETE" and route.startswith("/v1/jobs/") and "/artifacts/" in route:
            job_id, key = _split_artifact_route(route)
            return client.delete_job_artifact(job_id, key, **options)
        if method == "GET" and route.startswith("/v1/jobs/"):
            return client.get_job(route.removeprefix("/v1/jobs/"), **options)
        if method == "POST" and route == "/v1/jobs":
            body = _resolve_sdk_placeholders(request["body"], UBAG_SDK_NAME, UBAG_SDK_VERSION)
            return client.create_job(body, **options)
        if method == "POST" and route.endswith("/cancel"):
            job_id = route.removeprefix("/v1/jobs/").removesuffix("/cancel")
            return client.cancel_job(job_id, request.get("body") or {}, **options)
        if method == "POST" and route.endswith("/retry"):
            job_id = route.removeprefix("/v1/jobs/").removesuffix("/retry")
            return client.retry_job(job_id, request.get("body") or {}, **options)
        if method == "POST" and route == "/v1/webhooks/replay":
            body = _resolve_sdk_placeholders(request.get("body") or {}, UBAG_SDK_NAME, UBAG_SDK_VERSION)
            return client.replay_webhook_delivery(body, **options)
        if method == "GET" and route == "/v1/alerts":
            params = parse_qs(split.query)
            return client.list_alerts(
                limit=_int_or_none(_single(params, "limit")),
                status=_single(params, "status"),
                **options,
            )
        if method == "GET" and route == "/v1/alerts/config":
            return client.get_alert_config(**options)
        if method == "POST" and route.startswith("/v1/alerts/") and route.endswith("/acknowledge"):
            alert_id = route.removeprefix("/v1/alerts/").removesuffix("/acknowledge")
            body = _resolve_sdk_placeholders(request.get("body") or {}, UBAG_SDK_NAME, UBAG_SDK_VERSION)
            return client.acknowledge_alert(alert_id, body, **options)
        if method == "POST" and route.startswith("/v1/alerts/") and route.endswith("/resolve"):
            alert_id = route.removeprefix("/v1/alerts/").removesuffix("/resolve")
            body = _resolve_sdk_placeholders(request.get("body") or {}, UBAG_SDK_NAME, UBAG_SDK_VERSION)
            return client.resolve_alert(alert_id, body, **options)
        if method == "GET" and route == "/v1/browser/instances":
            params = parse_qs(split.query)
            return client.list_browser_instances(
                limit=_int_or_none(_single(params, "limit")),
                state=_single(params, "state"),
                **options,
            )
        if method == "GET" and route == "/v1/browser/contexts":
            params = parse_qs(split.query)
            return client.list_provider_contexts(
                limit=_int_or_none(_single(params, "limit")),
                instance_id=_single(params, "instance_id"),
                **options,
            )
        if method == "GET" and route == "/v1/browser/tabs":
            params = parse_qs(split.query)
            return client.list_browser_tabs(
                limit=_int_or_none(_single(params, "limit")),
                context_id=_single(params, "context_id"),
                state=_single(params, "state"),
                **options,
            )
        if method == "GET" and route == "/v1/browser/summary":
            return client.get_browser_topology_summary(**options)
        if method == "GET" and route == "/v1/concurrency":
            params = parse_qs(split.query)
            return client.get_concurrency(
                cursor=_single(params, "cursor"),
                limit=_int_or_none(_single(params, "limit")),
                **options,
            )
        if method == "POST" and route == "/v1/sso/logout":
            body = _resolve_sdk_placeholders(request.get("body") or {}, UBAG_SDK_NAME, UBAG_SDK_VERSION)
            return client.sso_logout(body, **options)
        if method == "POST" and route == "/v1/audit/export":
            body = _resolve_sdk_placeholders(request.get("body") or {}, UBAG_SDK_NAME, UBAG_SDK_VERSION)
            return client.export_audit(body, **options)

        self.fail("No SDK call mapping for {} {}".format(method, path))

    def _assert_recorded_request(
        self,
        scenario: Mapping[str, Any],
        recorded: Optional[Mapping[str, Any]],
    ) -> None:
        self.assertIsNotNone(recorded)
        assert recorded is not None

        expected_request = scenario["request"]
        self.assertEqual(recorded["method"], expected_request["method"])

        split = urlsplit(recorded["url"])
        actual_path = split.path + (("?" + split.query) if split.query else "")
        self.assertEqual(actual_path, expected_request["path"])

        actual_headers = {key.lower(): value for key, value in recorded["headers"].items()}
        for key, value in expected_request.get("headers", {}).items():
            self.assertEqual(actual_headers.get(key.lower()), value, key)

        if "body" in expected_request:
            assert recorded["body"] is not None
            actual_body = json.loads(recorded["body"].decode("utf-8"))
            expected_body = _resolve_sdk_placeholders(
                expected_request["body"], UBAG_SDK_NAME, UBAG_SDK_VERSION
            )
            self.assertEqual(actual_body, expected_body)
        if "body_text" in expected_request:
            assert recorded["body"] is not None
            self.assertEqual(recorded["body"].decode("utf-8"), expected_request["body_text"])

    def _assert_body_expectations(self, body: Any, expect: Mapping[str, Any]) -> None:
        self.assertTrue(expect.get("ok"))
        for key, value in expect.items():
            if not key.startswith("body."):
                continue
            self.assertEqual(_value_at_path(body, key.removeprefix("body.")), value, key)

    def _assert_error_expectations(
        self,
        error: UbagApiError,
        expect: Mapping[str, Any],
    ) -> None:
        if "error.code" in expect:
            self.assertEqual(error.code, expect["error.code"])
        if "error.retryable" in expect:
            self.assertEqual(error.retryable, expect["error.retryable"])
        if "error.retry_after_ms" in expect:
            self.assertEqual(error.retry_after_ms, expect["error.retry_after_ms"])


def _single(params: Mapping[str, list[str]], key: str) -> Optional[str]:
    values = params.get(key)
    return values[0] if values else None


def _split_artifact_route(route: str) -> Tuple[str, str]:
    body = route.removeprefix("/v1/jobs/")
    job_id, key = body.split("/artifacts/", 1)
    return unquote(job_id), unquote(key)


def _int_or_none(value: Optional[str]) -> Optional[int]:
    return int(value) if value is not None else None


def _value_at_path(value: Any, path: str) -> Any:
    current = value
    for part in path.split("."):
        if part == "length":
            current = len(current)
        elif isinstance(current, list) and part.isdigit():
            current = current[int(part)]
        else:
            current = current[part]
    return current


def _resolve_sdk_placeholders(value: Any, sdk_name: str, sdk_version: str) -> Any:
    if isinstance(value, dict):
        return {
            key: _resolve_sdk_placeholders(item, sdk_name, sdk_version)
            for key, item in value.items()
        }
    if isinstance(value, list):
        return [_resolve_sdk_placeholders(item, sdk_name, sdk_version) for item in value]
    if value == "__SDK_NAME__":
        return sdk_name
    if value == "__SDK_VERSION__":
        return sdk_version
    return value
