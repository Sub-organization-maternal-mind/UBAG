"""Google Antigravity SDK client wrapper.

Pinned to google-antigravity==0.1.17. Uses SDK-native compaction,
budgets, tool-output truncation, retry/backoff, and cancellation.
"""

from __future__ import annotations

import asyncio
import time
from dataclasses import dataclass, field
from typing import Any, AsyncGenerator, Dict, List, Mapping, Optional


class AntigravitySDKError(Exception):
    def __init__(self, message: str, *, retryable: bool = False, error_code: str = "") -> None:
        super().__init__(message)
        self.retryable = retryable
        self.error_code = error_code


@dataclass
class SDKConfig:
    api_key: str
    model: str = "gemini-3.8-flash"
    effort: str = "high"
    max_tokens: int = 65536
    vertex: bool = False
    project: str = ""
    location: str = "us-central1"
    sandbox: bool = False
    max_model_calls: int = 20
    max_tool_calls: int = 50
    max_input_tokens: int = 1048576
    max_output_tokens: int = 65536
    max_total_tokens: int = 200000
    compaction_enabled: bool = True
    compaction_threshold_tokens: int = 800000
    tool_output_max_chars: int = 10000
    retry_max_attempts: int = 3
    retry_base_delay: float = 1.0
    retry_max_delay: float = 60.0
    session_ttl_seconds: int = 3600


@dataclass
class SDKResponse:
    text: str = ""
    model: str = ""
    effort: str = ""
    conversation_id: str = ""
    token_usage: Dict[str, int] = field(default_factory=dict)
    tool_calls: List[Dict[str, Any]] = field(default_factory=list)
    stop_reason: str = ""
    structured_output: Any = None
    metadata: Dict[str, Any] = field(default_factory=dict)

    @property
    def total_tokens(self) -> int:
        return self.token_usage.get("total_tokens", 0)


class CircuitBreaker:
    def __init__(
        self,
        failure_threshold: int = 5,
        recovery_timeout: float = 60.0,
        half_open_max_calls: int = 1,
    ) -> None:
        self._failure_threshold = failure_threshold
        self._recovery_timeout = recovery_timeout
        self._half_open_max_calls = half_open_max_calls
        self._failures = 0
        self._last_failure: float = 0.0
        self._state = "closed"
        self._half_open_calls = 0

    @property
    def state(self) -> str:
        if self._state == "open":
            if time.monotonic() - self._last_failure >= self._recovery_timeout:
                self._state = "half_open"
                self._half_open_calls = 0
        return self._state

    def record_success(self) -> None:
        self._failures = 0
        self._state = "closed"
        self._half_open_calls = 0

    def record_failure(self) -> None:
        self._failures += 1
        self._last_failure = time.monotonic()
        if self._state == "half_open":
            self._state = "open"
        elif self._failures >= self._failure_threshold:
            self._state = "open"

    def can_execute(self) -> bool:
        state = self.state
        if state == "closed":
            return True
        if state == "half_open":
            if self._half_open_calls < self._half_open_max_calls:
                self._half_open_calls += 1
                return True
            return False
        return False


class AdaptiveRateLimiter:
    def __init__(
        self,
        initial_concurrency: int = 2,
        min_concurrency: int = 1,
        max_concurrency: int = 5,
        latency_threshold_ms: float = 10000.0,
    ) -> None:
        self._concurrency = initial_concurrency
        self._min_concurrency = min_concurrency
        self._max_concurrency = max_concurrency
        self._latency_threshold_ms = latency_threshold_ms
        self._semaphore = asyncio.Semaphore(initial_concurrency)
        self._recent_latencies: List[float] = []

    @property
    def concurrency(self) -> int:
        return self._concurrency

    async def acquire(self) -> None:
        await self._semaphore.acquire()

    def release(self) -> None:
        self._semaphore.release()

    def record_latency(self, latency_ms: float) -> None:
        self._recent_latencies.append(latency_ms)
        if len(self._recent_latencies) > 100:
            self._recent_latencies = self._recent_latencies[-100:]
        if latency_ms > self._latency_threshold_ms and self._concurrency > self._min_concurrency:
            self._concurrency = max(self._min_concurrency, self._concurrency - 1)
            self._semaphore = asyncio.Semaphore(self._concurrency)
        elif self._concurrency < self._max_concurrency:
            p95 = sorted(self._recent_latencies)[int(len(self._recent_latencies) * 0.95)]
            if p95 < self._latency_threshold_ms * 0.5:
                self._concurrency = min(self._max_concurrency, self._concurrency + 1)
                self._semaphore = asyncio.Semaphore(self._concurrency)

    def record_rate_limit(self) -> None:
        self._concurrency = max(self._min_concurrency, self._concurrency - 1)
        self._semaphore = asyncio.Semaphore(self._concurrency)


class ConversationSession:
    def __init__(
        self,
        conversation_id: str,
        agent: Any,
        config: SDKConfig,
        created_at: float,
    ) -> None:
        self.conversation_id = conversation_id
        self.agent = agent
        self.config = config
        self.created_at = created_at
        self.last_used = created_at
        self.active_turn = False
        self.turn_lock = asyncio.Lock()

    @property
    def is_expired(self) -> bool:
        return (time.monotonic() - self.last_used) > self.config.session_ttl_seconds

    @property
    def is_available(self) -> bool:
        return not self.active_turn and not self.is_expired


class SessionPool:
    def __init__(self, config: SDKConfig) -> None:
        self._config = config
        self._sessions: Dict[str, ConversationSession] = {}
        self._lock = asyncio.Lock()

    async def get_or_create(self, conversation_id: str) -> ConversationSession:
        async with self._lock:
            if conversation_id in self._sessions:
                session = self._sessions[conversation_id]
                if not session.is_expired:
                    session.last_used = time.monotonic()
                    return session
                await self._destroy_session(session)
                del self._sessions[conversation_id]

            agent = await self._create_agent()
            session = ConversationSession(
                conversation_id=conversation_id,
                agent=agent,
                config=self._config,
                created_at=time.monotonic(),
            )
            self._sessions[conversation_id] = session
            return session

    async def acquire_turn(self, conversation_id: str) -> Optional[ConversationSession]:
        session = await self.get_or_create(conversation_id)
        if session.is_available:
            await session.turn_lock.acquire()
            session.active_turn = True
            session.last_used = time.monotonic()
            return session
        return None

    async def release_turn(self, session: ConversationSession) -> None:
        session.active_turn = False
        session.last_used = time.monotonic()
        session.turn_lock.release()

    async def cleanup_expired(self) -> None:
        async with self._lock:
            expired = [
                cid for cid, s in self._sessions.items()
                if s.is_expired and not s.active_turn
            ]
            for cid in expired:
                await self._destroy_session(self._sessions[cid])
                del self._sessions[cid]

    async def _create_agent(self) -> Any:
        try:
            from google.antigravity import (
                Agent,
                AgentBehavior,
                BuiltinTools,
                CapabilitiesConfig,
                CompactionConfig,
                GeminiAPIEndpoint,
                GeminiModelOptions,
                LocalAgentConfig,
                ModelAPIRetryConfig,
                ModelTarget,
                ModelType,
                RetryConfig,
                ThinkingLevel,
                ToolOutputTruncationConfig,
                VertexEndpoint,
            )
            from google.antigravity.types import BudgetConfig
        except ImportError as exc:
            raise AntigravitySDKError(
                "google-antigravity==0.1.17 not installed. "
                "Run: pip install google-antigravity==0.1.17",
                retryable=False,
                error_code="SDK_NOT_INSTALLED",
            ) from exc

        options = GeminiModelOptions(thinking_level=ThinkingLevel(self._config.effort))
        if self._config.vertex:
            endpoint = (
                VertexEndpoint(project=self._config.project, location=self._config.location, options=options)
                if self._config.project
                else VertexEndpoint(api_key=self._config.api_key, options=options)
            )
        else:
            endpoint = GeminiAPIEndpoint(api_key=self._config.api_key, options=options)

        config = LocalAgentConfig(
            model=ModelTarget(name=self._config.model, types=[ModelType.TEXT], endpoint=endpoint),
            api_key=self._config.api_key if not self._config.vertex else None,
            capabilities=CapabilitiesConfig(
                enabled_tools=BuiltinTools.none(),
                enable_subagents=False,
                agent_behavior=AgentBehavior.MINIMAL,
                tool_output_truncation_config=ToolOutputTruncationConfig(
                    max_tokens=max(0, self._config.tool_output_max_chars // 4)
                ),
            ),
            budget_config=BudgetConfig(
                max_model_calls=self._config.max_model_calls,
                max_tool_calls=self._config.max_tool_calls,
                max_input_tokens=self._config.max_input_tokens,
                max_output_tokens=self._config.max_output_tokens,
                max_total_tokens=self._config.max_total_tokens,
            ),
            compaction_config=(
                CompactionConfig(token_threshold=min(
                    self._config.compaction_threshold_tokens,
                    self._config.max_total_tokens * 4 // 5,
                )) if self._config.compaction_enabled else None
            ),
            retry_config=RetryConfig(api_retry=ModelAPIRetryConfig(
                max_retries=max(0, self._config.retry_max_attempts - 1)
            )),
        )
        agent = Agent(config)
        await agent.__aenter__()
        return agent

    async def _destroy_session(self, session: ConversationSession) -> None:
        try:
            await session.agent.__aexit__(None, None, None)
        except Exception:
            pass

    async def shutdown(self) -> None:
        async with self._lock:
            for session in self._sessions.values():
                await self._destroy_session(session)
            self._sessions.clear()


class AntigravitySDKClient:
    def __init__(self, config: SDKConfig) -> None:
        self._config = config
        self._pool = SessionPool(config)
        self._circuit_breaker = CircuitBreaker()
        self._rate_limiter = AdaptiveRateLimiter()
        self._closed = False

    async def __aenter__(self) -> "AntigravitySDKClient":
        return self

    async def __aexit__(self, exc_type: Any, exc_val: Any, exc_tb: Any) -> None:
        await self.shutdown()

    async def shutdown(self) -> None:
        if not self._closed:
            await self._pool.shutdown()
            self._closed = True

    async def chat(
        self,
        prompt: str,
        *,
        model: Optional[str] = None,
        effort: Optional[str] = None,
        conversation_id: Optional[str] = None,
    ) -> SDKResponse:
        if self._closed:
            raise AntigravitySDKError(
                "Client is shut down",
                retryable=False,
                error_code="CLIENT_SHUT_DOWN",
            )

        if not self._circuit_breaker.can_execute():
            raise AntigravitySDKError(
                "Circuit breaker is open",
                retryable=True,
                error_code="CIRCUIT_BREAKER_OPEN",
            )

        await self._rate_limiter.acquire()
        start_time = time.monotonic()

        conv_id = conversation_id or "default"
        session = await self._pool.acquire_turn(conv_id)
        if session is None:
            self._rate_limiter.release()
            raise AntigravitySDKError(
                "Conversation is busy",
                retryable=True,
                error_code="CONVERSATION_BUSY",
            )

        try:
            model = model or self._config.model
            effort = effort or self._config.effort

            response = await session.agent.chat(prompt)

            text = await response.text() if hasattr(response, "text") else str(response)

            token_usage = self._extract_token_usage(response)
            tool_calls = await self._extract_tool_calls(response)
            stop_reason = self._extract_stop_reason(response)
            structured = await self._extract_structured_output(response)
            resp_conv_id = self._extract_conversation_id(session.agent) or self._extract_conversation_id(response) or conv_id
            used_model = self._extract_model(response) or model

            self._circuit_breaker.record_success()
            latency_ms = (time.monotonic() - start_time) * 1000
            self._rate_limiter.record_latency(latency_ms)

            return SDKResponse(
                text=text,
                model=used_model,
                effort=effort,
                conversation_id=resp_conv_id,
                token_usage=token_usage,
                tool_calls=tool_calls,
                stop_reason=stop_reason,
                structured_output=structured,
                metadata=self._extract_metadata(response),
            )
        except AntigravitySDKError:
            self._circuit_breaker.record_failure()
            self._rate_limiter.record_rate_limit()
            raise
        except Exception as exc:
            self._circuit_breaker.record_failure()
            self._rate_limiter.record_rate_limit()
            error_str = str(exc).lower()
            retryable = any(
                keyword in error_str
                for keyword in ("timeout", "rate limit", "quota", "overload", "503", "429")
            )
            raise AntigravitySDKError(
                f"Antigravity SDK chat error: {exc}",
                retryable=retryable,
                error_code="CHAT_ERROR",
            ) from exc
        finally:
            await self._pool.release_turn(session)
            self._rate_limiter.release()

    async def chat_stream(
        self,
        prompt: str,
        *,
        model: Optional[str] = None,
        effort: Optional[str] = None,
        conversation_id: Optional[str] = None,
    ) -> AsyncGenerator[str, None]:
        if self._closed:
            raise AntigravitySDKError(
                "Client is shut down",
                retryable=False,
                error_code="CLIENT_SHUT_DOWN",
            )

        if not self._circuit_breaker.can_execute():
            raise AntigravitySDKError(
                "Circuit breaker is open",
                retryable=True,
                error_code="CIRCUIT_BREAKER_OPEN",
            )

        await self._rate_limiter.acquire()
        start_time = time.monotonic()

        conv_id = conversation_id or "default"
        session = await self._pool.acquire_turn(conv_id)
        if session is None:
            self._rate_limiter.release()
            raise AntigravitySDKError(
                "Conversation is busy",
                retryable=True,
                error_code="CONVERSATION_BUSY",
            )

        try:
            model = model or self._config.model
            effort = effort or self._config.effort

            response = await session.agent.chat(prompt)

            async for text in response:
                if text:
                    yield text

            self._circuit_breaker.record_success()
            latency_ms = (time.monotonic() - start_time) * 1000
            self._rate_limiter.record_latency(latency_ms)
        except AntigravitySDKError:
            self._circuit_breaker.record_failure()
            self._rate_limiter.record_rate_limit()
            raise
        except Exception as exc:
            self._circuit_breaker.record_failure()
            self._rate_limiter.record_rate_limit()
            error_str = str(exc).lower()
            retryable = any(
                keyword in error_str
                for keyword in ("timeout", "rate limit", "quota", "overload", "503", "429")
            )
            raise AntigravitySDKError(
                f"Antigravity SDK stream error: {exc}",
                retryable=retryable,
                error_code="STREAM_ERROR",
            ) from exc
        finally:
            await self._pool.release_turn(session)
            self._rate_limiter.release()

    def _extract_token_usage(self, response: Any) -> Dict[str, int]:
        usage: Dict[str, int] = {}
        try:
            if hasattr(response, "usage_metadata"):
                meta = response.usage_metadata
                if hasattr(meta, "prompt_token_count"):
                    usage["input_tokens"] = int(meta.prompt_token_count or 0)
                if hasattr(meta, "candidates_token_count"):
                    usage["output_tokens"] = int(meta.candidates_token_count or 0)
                if hasattr(meta, "thoughts_token_count"):
                    usage["thinking_tokens"] = int(meta.thoughts_token_count or 0)
                if hasattr(meta, "cached_content_token_count"):
                    usage["cached_tokens"] = int(meta.cached_content_token_count or 0)
                if hasattr(meta, "total_token_count"):
                    usage["total_tokens"] = int(meta.total_token_count or 0)
            elif hasattr(response, "usage"):
                meta = response.usage
                if isinstance(meta, Mapping):
                    usage["input_tokens"] = int(meta.get("input_tokens", 0))
                    usage["output_tokens"] = int(meta.get("output_tokens", 0))
                    usage["thinking_tokens"] = int(meta.get("thinking_tokens", 0))
                    usage["cached_tokens"] = int(meta.get("cached_tokens", 0))
                    usage["total_tokens"] = int(meta.get("total_tokens", 0))
        except (TypeError, ValueError, AttributeError):
            pass

        if "total_tokens" not in usage:
            usage["total_tokens"] = (
                usage.get("input_tokens", 0)
                + usage.get("output_tokens", 0)
                + usage.get("thinking_tokens", 0)
            )
        return usage

    async def _extract_tool_calls(self, response: Any) -> List[Dict[str, Any]]:
        calls: List[Dict[str, Any]] = []
        try:
            if hasattr(response, "tool_calls"):
                async for tc in response.tool_calls:
                    calls.append({
                        "tool_call_id": str(tc.id or ""),
                        "tool_name": str(tc.name or ""),
                        "arguments": str(tc.args or ""),
                    })
        except (TypeError, ValueError, AttributeError):
            pass
        return calls

    def _extract_stop_reason(self, response: Any) -> str:
        try:
            if hasattr(response, "stop_reason"):
                return str(response.stop_reason or "")
            if hasattr(response, "finish_reason"):
                return str(response.finish_reason or "")
        except (TypeError, ValueError):
            pass
        return ""

    async def _extract_structured_output(self, response: Any) -> Any:
        try:
            structured_output = getattr(response, "structured_output", None)
            if callable(structured_output):
                return await structured_output()
            if structured_output is not None:
                return structured_output
            if hasattr(response, "parsed"):
                return response.parsed
        except (TypeError, ValueError):
            pass
        return None

    def _extract_conversation_id(self, response: Any) -> str:
        try:
            if hasattr(response, "conversation_id"):
                return str(response.conversation_id or "")
            if hasattr(response, "session_id"):
                return str(response.session_id or "")
        except (TypeError, ValueError):
            pass
        return ""

    def _extract_model(self, response: Any) -> str:
        try:
            if hasattr(response, "model"):
                return str(response.model or "")
            if hasattr(response, "model_name"):
                return str(response.model_name or "")
        except (TypeError, ValueError):
            pass
        return ""

    def _extract_metadata(self, response: Any) -> Dict[str, Any]:
        meta: Dict[str, Any] = {}
        try:
            if hasattr(response, "metadata"):
                raw = response.metadata
                if isinstance(raw, Mapping):
                    meta.update({str(k): v for k, v in raw.items()})
        except (TypeError, ValueError):
            pass
        return meta

    @staticmethod
    def run_sync(
        config: SDKConfig,
        prompt: str,
        *,
        model: Optional[str] = None,
        effort: Optional[str] = None,
    ) -> SDKResponse:
        client = AntigravitySDKClient(config)

        async def _run() -> SDKResponse:
            async with client:
                return await client.chat(prompt, model=model, effort=effort)

        try:
            loop = asyncio.get_event_loop()
            if loop.is_running():
                loop = asyncio.new_event_loop()
                asyncio.set_event_loop(loop)
            return loop.run_until_complete(_run())
        except RuntimeError:
            loop = asyncio.new_event_loop()
            asyncio.set_event_loop(loop)
            return loop.run_until_complete(_run())
