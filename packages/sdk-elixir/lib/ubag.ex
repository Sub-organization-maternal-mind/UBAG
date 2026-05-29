defmodule Ubag do
  @moduledoc """
  Elixir client for the UBAG v0 REST gateway.

  Mirrors the canonical Go SDK contract: stable `UBAG-` error codes, automatic
  `Idempotency-Key` generation for mutating calls, SDK metadata headers, and a
  pluggable `Ubag.Transport` for testing without a live gateway.
  """

  alias Ubag.{ApiError, Idempotency, JSON, Transport, TransportError}

  @api_version "2026-05-22"
  @sdk_name "ubag-elixir"
  @sdk_version "0.0.0"
  @json_content_type "application/json"

  @enforce_keys [:base_url, :api_version, :transport]
  defstruct [:base_url, :api_version, :app_secret, :transport, default_headers: %{}]

  @type t :: %__MODULE__{}

  def api_version, do: @api_version
  def sdk_name, do: @sdk_name
  def sdk_version, do: @sdk_version

  @doc """
  Builds a client.

  Options: `:app_secret`, `:api_version`, `:transport`, `:default_headers`.
  """
  @spec new(binary(), keyword()) :: t()
  def new(base_url, opts \\ []) do
    trimmed = String.trim(base_url)

    if trimmed == "", do: raise(ArgumentError, "base_url is required")
    unless String.contains?(trimmed, "://"), do: raise(ArgumentError, "base_url must include scheme and host")

    %__MODULE__{
      base_url: String.trim_trailing(trimmed, "/"),
      api_version: Keyword.get(opts, :api_version, @api_version),
      app_secret: Keyword.get(opts, :app_secret),
      transport: Keyword.get(opts, :transport, Transport.Httpc),
      default_headers: Keyword.get(opts, :default_headers, %{})
    }
  end

  # --- System -----------------------------------------------------------

  def health(client, opts \\ []), do: request_json(client, "GET", "/v1/health", nil, opts)
  def ready(client, opts \\ []), do: request_json(client, "GET", "/v1/ready", nil, opts)

  def version(client, opts \\ []) do
    request_json(client, "GET", "/v1/version", nil, Keyword.delete(opts, :idempotency_key))
  end

  def metrics(client, opts \\ []) do
    opts = put_header(opts, "Accept", "text/plain")
    send_request(client, "GET", "/v1/metrics", nil, nil, opts).body
  end

  # --- Jobs --------------------------------------------------------------

  def create_job(client, request, opts \\ []) when is_map(request) do
    api_version = string_field(request, "api_version") || opts[:api_version] || client.api_version
    key = string_field(request, "idempotency_key") || opts[:idempotency_key] || Idempotency.generate()

    body =
      request
      |> Map.put("api_version", api_version)
      |> Map.put("idempotency_key", key)
      |> ensure_sdk_metadata()

    opts = opts |> Keyword.put(:api_version, api_version) |> Keyword.put(:idempotency_key, key)
    request_json(client, "POST", "/v1/jobs", body, opts)
  end

  def get_job(client, job_id, opts \\ []) do
    request_json(client, "GET", "/v1/jobs/#{encode(job_id)}", nil, opts)
  end

  def list_jobs(client, params \\ %{}, opts \\ []) do
    pairs =
      []
      |> add_pair("cursor", params[:cursor])
      |> add_pair("limit", params[:limit])
      |> add_pair("filter[status]", params[:status])
      |> add_pair("filter[target]", params[:target])
      |> add_pair("sort", params[:sort])
      |> add_pair("fields", join(params[:fields]))
      |> add_pair("include", join(params[:include]))

    request_json(client, "GET", "/v1/jobs" <> encode_query(pairs), nil, opts)
  end

  def cancel_job(client, job_id, request \\ %{}, opts \\ []) do
    mutate(client, "/v1/jobs/#{encode(job_id)}/cancel", request, opts)
  end

  def retry_job(client, job_id, request \\ %{}, opts \\ []) do
    mutate(client, "/v1/jobs/#{encode(job_id)}/retry", request, opts)
  end

  # --- Job events --------------------------------------------------------

  def list_job_events(client, job_id, params \\ %{}, opts \\ []) do
    pairs =
      []
      |> add_pair("cursor", params[:cursor])
      |> add_pair("after_sequence", params[:after_sequence])
      |> add_pair("limit", params[:limit])

    request_json(client, "GET", "/v1/jobs/#{encode(job_id)}/events" <> encode_query(pairs), nil, opts)
  end

  def stream_job_events_sse(client, job_id, opts \\ []) do
    opts = put_header(opts, "Accept", "text/event-stream")
    send_request(client, "GET", "/v1/sse/jobs/#{encode(job_id)}", nil, nil, opts).body
  end

  # --- Artifacts ---------------------------------------------------------

  def list_job_artifacts(client, job_id, opts \\ []) do
    request_json(client, "GET", "/v1/jobs/#{encode(job_id)}/artifacts", nil, opts)
  end

  def get_job_artifact(client, job_id, key, opts \\ []) do
    response = send_request(client, "GET", "/v1/jobs/#{encode(job_id)}/artifacts/#{encode(key)}", nil, nil, opts)

    %{
      body: response.body,
      content_type: Map.get(response.headers, "content-type", ""),
      checksum: Map.get(response.headers, "ubag-artifact-checksum", "")
    }
  end

  def put_job_artifact(client, job_id, key, body, content_type \\ "application/octet-stream", opts \\ []) do
    opts = Keyword.put_new_lazy(opts, :idempotency_key, &Idempotency.generate/0)
    resolved_type = if content_type in [nil, ""], do: "application/octet-stream", else: content_type
    response = send_request(client, "PUT", "/v1/jobs/#{encode(job_id)}/artifacts/#{encode(key)}", body, resolved_type, opts)
    decode_json(response.body)
  end

  def delete_job_artifact(client, job_id, key, opts \\ []) do
    opts = Keyword.put_new_lazy(opts, :idempotency_key, &Idempotency.generate/0)
    send_request(client, "DELETE", "/v1/jobs/#{encode(job_id)}/artifacts/#{encode(key)}", nil, nil, opts)
    :ok
  end

  # --- Operator collections ---------------------------------------------

  def list_workflows(client, opts \\ []), do: request_json(client, "GET", "/v1/workflows", nil, opts)
  def list_templates(client, opts \\ []), do: request_json(client, "GET", "/v1/templates", nil, opts)

  def list_targets(client, params \\ %{}, opts \\ []),
    do: request_json(client, "GET", "/v1/targets" <> build_list_query(params), nil, opts)

  def list_adapters(client, params \\ %{}, opts \\ []),
    do: request_json(client, "GET", "/v1/adapters" <> build_list_query(params), nil, opts)

  def list_apps(client, params \\ %{}, opts \\ []),
    do: request_json(client, "GET", "/v1/apps" <> build_list_query(params), nil, opts)

  def list_devices(client, params \\ %{}, opts \\ []),
    do: request_json(client, "GET", "/v1/devices" <> build_list_query(params), nil, opts)

  def list_webhooks(client, params \\ %{}, opts \\ []),
    do: request_json(client, "GET", "/v1/webhooks" <> build_list_query(params), nil, opts)

  def list_audit_events(client, params \\ %{}, opts \\ []),
    do: request_json(client, "GET", "/v1/audit" <> build_list_query(params), nil, opts)

  def list_events(client, params \\ %{}, opts \\ []),
    do: request_json(client, "GET", "/v1/events" <> build_list_query(params), nil, opts)

  def replay_webhook_delivery(client, request \\ %{}, opts \\ []),
    do: mutate(client, "/v1/webhooks/replay", request, opts)

  def cache_status(client, opts \\ []), do: request_json(client, "GET", "/v1/cache", nil, opts)

  # --- Internal helpers --------------------------------------------------

  defp mutate(client, path, request, opts) do
    api_version = string_field(request, "api_version") || opts[:api_version] || client.api_version
    key = string_field(request, "idempotency_key") || opts[:idempotency_key] || Idempotency.generate()

    body =
      request
      |> Map.put("api_version", api_version)
      |> Map.put("idempotency_key", key)

    opts = opts |> Keyword.put(:api_version, api_version) |> Keyword.put(:idempotency_key, key)
    request_json(client, "POST", path, body, opts)
  end

  defp request_json(client, method, path, body, opts) do
    {serialized, content_type} =
      case body do
        nil -> {nil, nil}
        map -> {JSON.encode(map), @json_content_type}
      end

    response = send_request(client, method, path, serialized, content_type, opts)

    if response.body == "" or response.status == 204 do
      %{}
    else
      decode_json(response.body)
    end
  end

  defp send_request(client, method, path, body, content_type, opts) do
    url = client.base_url <> path
    api_version = opts[:api_version] || client.api_version

    headers =
      %{
        "Accept" => @json_content_type,
        "Ubag-Api-Version" => api_version,
        "Ubag-Sdk-Name" => @sdk_name,
        "Ubag-Sdk-Version" => @sdk_version
      }
      |> Map.merge(stringify(client.default_headers))
      |> Map.merge(stringify(opts[:headers] || %{}))

    headers =
      if client.app_secret && not Map.has_key?(headers, "Authorization") do
        Map.put(headers, "Authorization", "Bearer " <> client.app_secret)
      else
        headers
      end

    headers =
      case opts[:idempotency_key] do
        key when is_binary(key) and key != "" -> Map.put(headers, "Idempotency-Key", key)
        _ -> headers
      end

    headers =
      if body not in [nil, ""] or content_type != nil do
        Map.put(headers, "Content-Type", content_type || @json_content_type)
      else
        headers
      end

    request = %{method: method, url: url, headers: Map.to_list(headers), body: body}

    case call_transport(client.transport, request) do
      {:ok, response} ->
        if response.status < 200 or response.status >= 300 do
          raw_body = response.body

          raise %ApiError{
            status: response.status,
            method: method,
            url: url,
            headers: response.headers,
            raw_body: raw_body,
            envelope: parse_envelope(raw_body)
          }
        end

        response

      {:error, reason} ->
        raise %TransportError{method: method, url: url, reason: reason}
    end
  end

  defp call_transport(fun, request) when is_function(fun, 1), do: fun.(request)
  defp call_transport(module, request) when is_atom(module), do: module.send(request)

  defp parse_envelope(""), do: nil

  defp parse_envelope(raw_body) do
    case JSON.decode(raw_body) do
      {:ok, %{"error" => %{"code" => code}} = parsed} when is_binary(code) ->
        if String.starts_with?(code, "UBAG-"), do: parsed, else: nil

      _ ->
        nil
    end
  end

  defp decode_json(""), do: %{}

  defp decode_json(body) do
    case JSON.decode(body) do
      {:ok, %{} = map} -> map
      _ -> %{}
    end
  end

  defp ensure_sdk_metadata(body) do
    sdk = %{"name" => @sdk_name, "version" => @sdk_version}

    case Map.get(body, "client") do
      %{} = client ->
        if Map.has_key?(client, "sdk"),
          do: body,
          else: Map.put(body, "client", Map.put(client, "sdk", sdk))

      _ ->
        Map.put(body, "client", %{"sdk" => sdk})
    end
  end

  defp string_field(map, key) do
    case Map.get(map, key) do
      value when is_binary(value) and value != "" -> value
      _ -> nil
    end
  end

  defp put_header(opts, name, value) do
    headers = (opts[:headers] || %{}) |> stringify() |> Map.put(name, value)
    Keyword.put(opts, :headers, headers)
  end

  defp stringify(headers) when is_map(headers) do
    Map.new(headers, fn {k, v} -> {to_string(k), to_string(v)} end)
  end

  defp stringify(headers) when is_list(headers) do
    Map.new(headers, fn {k, v} -> {to_string(k), to_string(v)} end)
  end

  defp add_pair(pairs, _key, nil), do: pairs
  defp add_pair(pairs, _key, ""), do: pairs
  defp add_pair(pairs, _key, value) when is_integer(value) and value <= 0, do: pairs
  defp add_pair(pairs, key, value), do: pairs ++ [{key, to_string(value)}]

  defp join(nil), do: nil
  defp join(list) when is_list(list), do: Enum.join(list, ",")
  defp join(value), do: value

  defp build_list_query(params) do
    []
    |> add_pair("cursor", params[:cursor])
    |> add_pair("limit", params[:limit])
    |> encode_query()
  end

  defp encode_query([]), do: ""

  defp encode_query(pairs) do
    "?" <> Enum.map_join(pairs, "&", fn {k, v} -> URI.encode_www_form(k) <> "=" <> URI.encode_www_form(v) end)
  end

  defp encode(value), do: URI.encode_www_form(to_string(value))
end
