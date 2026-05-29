defmodule Ubag.Transport do
  @moduledoc """
  Behaviour for pluggable HTTP transports.

  The default implementation (`Ubag.Transport.Httpc`) uses the built-in
  `:httpc` client. Tests supply a capturing implementation to assert request
  construction; Req or Finch can be wrapped without adding a dependency.
  """

  @type request :: %{
          method: binary(),
          url: binary(),
          headers: [{binary(), binary()}],
          body: binary() | nil
        }

  @type response :: %{status: integer(), headers: map(), body: binary()}

  @callback send(request()) :: {:ok, response()} | {:error, term()}
end

defmodule Ubag.Transport.Httpc do
  @moduledoc "Default `Ubag.Transport` backed by Erlang's `:httpc`."

  @behaviour Ubag.Transport

  @impl true
  def send(%{method: method, url: url, headers: headers, body: body}) do
    :inets.start()
    :ssl.start()

    http_method = method |> String.downcase() |> String.to_atom()
    erl_headers = Enum.map(headers, fn {k, v} -> {String.to_charlist(k), String.to_charlist(v)} end)
    erl_url = String.to_charlist(url)

    request =
      if body in [nil, ""] and http_method in [:get, :delete, :head] do
        {erl_url, erl_headers}
      else
        content_type =
          headers
          |> Enum.find_value("application/json", fn {k, v} ->
            if String.downcase(k) == "content-type", do: v
          end)

        {erl_url, erl_headers, String.to_charlist(content_type), body || ""}
      end

    case :httpc.request(http_method, request, [], body_format: :binary) do
      {:ok, {{_version, status, _reason}, response_headers, response_body}} ->
        decoded_headers =
          response_headers
          |> Enum.map(fn {k, v} -> {k |> List.to_string() |> String.downcase(), List.to_string(v)} end)
          |> Map.new()

        {:ok, %{status: status, headers: decoded_headers, body: to_binary(response_body)}}

      {:error, reason} ->
        {:error, reason}
    end
  end

  defp to_binary(value) when is_binary(value), do: value
  defp to_binary(value) when is_list(value), do: List.to_string(value)
end
