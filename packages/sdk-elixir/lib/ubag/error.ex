defmodule Ubag.ApiError do
  @moduledoc "Raised when the gateway returns a non-2xx response."

  defexception [:status, :method, :url, :headers, :raw_body, :envelope]

  @type t :: %__MODULE__{
          status: integer(),
          method: binary(),
          url: binary(),
          headers: map(),
          raw_body: binary(),
          envelope: map() | nil
        }

  @impl true
  def message(%__MODULE__{} = error) do
    case error.envelope do
      %{"error" => %{"message" => message}} when is_binary(message) and message != "" ->
        message

      _ ->
        "UBAG API request failed with HTTP #{error.status}"
    end
  end

  @spec code(t()) :: binary() | nil
  def code(%__MODULE__{} = error), do: error_field(error, "code")

  @spec category(t()) :: binary() | nil
  def category(%__MODULE__{} = error), do: error_field(error, "category")

  @spec retryable?(t()) :: boolean()
  def retryable?(%__MODULE__{envelope: %{"error" => %{"retryable" => value}}}) when is_boolean(value),
    do: value

  def retryable?(%__MODULE__{}), do: false

  @spec trace_id(t()) :: binary() | nil
  def trace_id(%__MODULE__{} = error) do
    cond do
      (value = error_field(error, "trace_id")) != nil -> value
      (value = Map.get(error.headers, "ubag-trace-id")) not in [nil, ""] -> value
      (value = Map.get(error.headers, "x-request-id")) not in [nil, ""] -> value
      true -> nil
    end
  end

  defp error_field(%__MODULE__{envelope: %{"error" => fields}}, name) when is_map(fields) do
    case Map.get(fields, name) do
      value when is_binary(value) and value != "" -> value
      _ -> nil
    end
  end

  defp error_field(%__MODULE__{}, _name), do: nil
end

defmodule Ubag.TransportError do
  @moduledoc "Raised when a request could not be sent (network/transport failure)."

  defexception [:method, :url, :reason]

  @impl true
  def message(%__MODULE__{} = error) do
    "UBAG API request could not be sent: #{error.method} #{error.url}: #{inspect(error.reason)}"
  end
end
