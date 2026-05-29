# UBAG Elixir SDK

Idiomatic Elixir client for the UBAG v0 REST gateway, with **no external
dependencies** (built-in `:httpc` transport + a tiny `Ubag.JSON` codec). Mirrors
the canonical [`sdk-go`](../sdk-go) contract: stable `UBAG-` error codes,
automatic `Idempotency-Key` generation for mutating calls, SDK metadata headers,
and a pluggable `Ubag.Transport` for testing without a live gateway.

- API version header: `Ubag-Api-Version: 2026-05-22`
- SDK headers: `Ubag-Sdk-Name: ubag-elixir`, `Ubag-Sdk-Version: 0.0.0`
- Auth: `Authorization: Bearer <app-secret>`

## Requirements

- Elixir ~> 1.14

## Usage

```elixir
client = Ubag.new("https://gateway.example.com", app_secret: System.get_env("UBAG_APP_SECRET"))

{:ok, _} = {:ok, Ubag.health(client)}

job = Ubag.create_job(client, %{
  "job" => %{"target" => "mock_target", "command_type" => "echo"}
})

page = Ubag.list_jobs(client, %{status: "completed", limit: 20})

artifact = Ubag.get_job_artifact(client, job["id"], "report.txt")
IO.puts("#{artifact.content_type} #{artifact.checksum}")
```

Successful calls return decoded maps (or a binary for `metrics/2` and
`stream_job_events_sse/3`, and a `%{body:, content_type:, checksum:}` map for
`get_job_artifact/4`). Failures raise `Ubag.ApiError` or `Ubag.TransportError`.

### Error handling

```elixir
try do
  Ubag.cancel_job(client, "job_123", %{"reason" => "caller_cancelled"})
rescue
  error in Ubag.ApiError ->
    # error.status, Ubag.ApiError.code(error), Ubag.ApiError.category(error),
    # Ubag.ApiError.retryable?(error), Ubag.ApiError.trace_id(error)
  error in Ubag.TransportError ->
    # network/transport failure before a response was received
end
```

### Custom transport

Pass a 1-arity function or a module implementing the `Ubag.Transport` behaviour
to integrate Req, Finch, or a test double:

```elixir
transport = fn request ->
  # return {:ok, %{status: integer, headers: map, body: binary}} | {:error, term}
end

client = Ubag.new("https://gateway.example.com", transport: transport)
```

## Testing

```bash
mix test
```

Tests inject a capturing transport and assert request method, path, headers, and
body without contacting a gateway. Builds and tests **fully offline** — no Hex
dependencies to fetch.
