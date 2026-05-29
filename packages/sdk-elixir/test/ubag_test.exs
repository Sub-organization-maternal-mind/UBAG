defmodule UbagTest do
  use ExUnit.Case, async: true

  alias Ubag.{ApiError, JSON}

  # Capturing transport: records the request into an Agent and returns a canned
  # response. Asserts request construction without contacting a gateway.
  defp capturing(agent, status, body, headers \\ %{}) do
    fn request ->
      Agent.update(agent, fn _ -> request end)
      {:ok, %{status: status, headers: headers, body: body}}
    end
  end

  defp client(transport) do
    Ubag.new("http://127.0.0.1:7878/", app_secret: "app_secret_fixture", transport: transport)
  end

  defp headers_map(request), do: Map.new(request.headers)

  setup do
    {:ok, agent} = start_supervised({Agent, fn -> nil end})
    %{agent: agent}
  end

  test "health sends version and auth headers", %{agent: agent} do
    result = client(capturing(agent, 200, ~s({"status":"ok"}))) |> Ubag.health()

    assert result["status"] == "ok"
    request = Agent.get(agent, & &1)
    headers = headers_map(request)
    assert request.method == "GET"
    assert request.url == "http://127.0.0.1:7878/v1/health"
    assert headers["Ubag-Api-Version"] == Ubag.api_version()
    assert headers["Ubag-Sdk-Name"] == Ubag.sdk_name()
    assert headers["Authorization"] == "Bearer app_secret_fixture"
    assert request.body == nil
  end

  test "version omits idempotency key", %{agent: agent} do
    client(capturing(agent, 200, ~s({"version":"0.0.0"}))) |> Ubag.version(idempotency_key: "ignored")

    request = Agent.get(agent, & &1)
    assert request.url == "http://127.0.0.1:7878/v1/version"
    refute Map.has_key?(headers_map(request), "Idempotency-Key")
  end

  test "create_job injects version, idempotency and sdk metadata", %{agent: agent} do
    body = %{
      "client" => %{"app_id" => "fixture-app", "app_version" => "0.0.0"},
      "job" => %{"target" => "mock_target", "command_type" => "echo"}
    }

    client(capturing(agent, 202, ~s({"status":"queued"})))
    |> Ubag.create_job(body, idempotency_key: "idem_elixir_sdk")

    request = Agent.get(agent, & &1)
    headers = headers_map(request)
    assert request.method == "POST"
    assert request.url == "http://127.0.0.1:7878/v1/jobs"
    assert headers["Idempotency-Key"] == "idem_elixir_sdk"
    assert headers["Content-Type"] == "application/json"

    sent = JSON.decode!(request.body)
    assert sent["api_version"] == Ubag.api_version()
    assert sent["idempotency_key"] == "idem_elixir_sdk"
    assert sent["client"]["sdk"]["name"] == Ubag.sdk_name()
    assert sent["client"]["sdk"]["version"] == Ubag.sdk_version()
  end

  test "create_job generates idempotency key when missing", %{agent: agent} do
    client(capturing(agent, 202, ~s({"status":"queued"})))
    |> Ubag.create_job(%{"job" => %{"target" => "mock_target"}})

    request = Agent.get(agent, & &1)
    key = headers_map(request)["Idempotency-Key"]
    assert String.length(key) == 26
    assert JSON.decode!(request.body)["idempotency_key"] == key
  end

  test "list_jobs builds filter query", %{agent: agent} do
    client(capturing(agent, 200, ~s({"jobs":[]})))
    |> Ubag.list_jobs(%{cursor: "cursor_1", limit: 1, status: "completed"})

    request = Agent.get(agent, & &1)
    assert request.url == "http://127.0.0.1:7878/v1/jobs?cursor=cursor_1&limit=1&filter%5Bstatus%5D=completed"
  end

  test "cancel_job is idempotent post", %{agent: agent} do
    client(capturing(agent, 202, ~s({"status":"cancelled"})))
    |> Ubag.cancel_job("job_1", %{"reason" => "caller_cancelled"}, idempotency_key: "idem_cancel")

    request = Agent.get(agent, & &1)
    headers = headers_map(request)
    assert request.method == "POST"
    assert request.url == "http://127.0.0.1:7878/v1/jobs/job_1/cancel"
    assert headers["Idempotency-Key"] == "idem_cancel"
    sent = JSON.decode!(request.body)
    assert sent["idempotency_key"] == "idem_cancel"
    assert sent["reason"] == "caller_cancelled"
  end

  test "put_artifact sends bytes and generates key", %{agent: agent} do
    client(capturing(agent, 201, ~s({"idempotent_replay":false})))
    |> Ubag.put_job_artifact("job_1", "report.txt", "hello artifact", "text/plain")

    request = Agent.get(agent, & &1)
    headers = headers_map(request)
    assert request.method == "PUT"
    assert request.url == "http://127.0.0.1:7878/v1/jobs/job_1/artifacts/report.txt"
    assert headers["Content-Type"] == "text/plain"
    assert String.length(headers["Idempotency-Key"]) == 26
    assert request.body == "hello artifact"
  end

  test "get_artifact returns bytes and checksum", %{agent: agent} do
    transport =
      capturing(agent, 200, "hello artifact", %{
        "content-type" => "text/plain",
        "ubag-artifact-checksum" => "sha256_fixture"
      })

    download = client(transport) |> Ubag.get_job_artifact("job_1", "report.txt")

    assert download.body == "hello artifact"
    assert download.content_type == "text/plain"
    assert download.checksum == "sha256_fixture"
  end

  test "metrics request sets text accept", %{agent: agent} do
    text = client(capturing(agent, 200, "ubag_gateway_requests_total 1\n")) |> Ubag.metrics()

    assert text == "ubag_gateway_requests_total 1\n"
    assert headers_map(Agent.get(agent, & &1))["Accept"] == "text/plain"
  end

  test "api error envelope is parsed", %{agent: agent} do
    envelope =
      ~s({"error":{"code":"UBAG-AUTH-MISSING-001","category":"auth",) <>
        ~s("message":"No supported credential was provided","retryable":false,) <>
        ~s("trace_id":"trace_auth_missing"}})

    error =
      assert_raise ApiError, fn ->
        client(capturing(agent, 401, envelope)) |> Ubag.list_workflows()
      end

    assert error.status == 401
    assert ApiError.code(error) == "UBAG-AUTH-MISSING-001"
    assert ApiError.category(error) == "auth"
    refute ApiError.retryable?(error)
    assert ApiError.trace_id(error) == "trace_auth_missing"
    assert Exception.message(error) =~ "No supported credential"
  end
end
