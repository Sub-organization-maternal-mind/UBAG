defmodule Ubag.JSON do
  @moduledoc """
  Minimal JSON codec with no external dependencies.

  Supports the subset required by the UBAG gateway contract: objects, arrays,
  strings, integers, floats, booleans and `null`. Object keys are decoded as
  strings.
  """

  @doc "Encodes an Elixir term into a JSON binary."
  @spec encode(term()) :: binary()
  def encode(term), do: encode_value(term)

  @doc "Decodes a JSON binary, returning `{:ok, value}` or `{:error, reason}`."
  @spec decode(binary()) :: {:ok, term()} | {:error, term()}
  def decode(binary) when is_binary(binary) do
    {value, rest} = parse_value(skip_ws(binary))

    case skip_ws(rest) do
      "" -> {:ok, value}
      _ -> {:ok, value}
    end
  rescue
    error -> {:error, error}
  end

  @doc "Decodes a JSON binary, raising on invalid input."
  @spec decode!(binary()) :: term()
  def decode!(binary) do
    case decode(binary) do
      {:ok, value} -> value
      {:error, error} -> raise ArgumentError, "invalid JSON: #{inspect(error)}"
    end
  end

  # --- Encoding ---------------------------------------------------------

  defp encode_value(nil), do: "null"
  defp encode_value(true), do: "true"
  defp encode_value(false), do: "false"
  defp encode_value(value) when is_integer(value), do: Integer.to_string(value)
  defp encode_value(value) when is_float(value), do: Float.to_string(value)
  defp encode_value(value) when is_binary(value), do: encode_string(value)
  defp encode_value(value) when is_atom(value), do: encode_string(Atom.to_string(value))

  defp encode_value(value) when is_list(value) do
    "[" <> Enum.map_join(value, ",", &encode_value/1) <> "]"
  end

  defp encode_value(value) when is_map(value) do
    inner =
      value
      |> Enum.map_join(",", fn {key, val} ->
        encode_string(to_key(key)) <> ":" <> encode_value(val)
      end)

    "{" <> inner <> "}"
  end

  defp to_key(key) when is_atom(key), do: Atom.to_string(key)
  defp to_key(key) when is_binary(key), do: key

  defp encode_string(string) do
    escaped =
      string
      |> String.replace("\\", "\\\\")
      |> String.replace("\"", "\\\"")
      |> String.replace("\n", "\\n")
      |> String.replace("\r", "\\r")
      |> String.replace("\t", "\\t")

    "\"" <> escaped <> "\""
  end

  # --- Decoding ---------------------------------------------------------

  defp skip_ws(<<c, rest::binary>>) when c in [?\s, ?\t, ?\n, ?\r], do: skip_ws(rest)
  defp skip_ws(binary), do: binary

  defp parse_value(<<"{", rest::binary>>), do: parse_object(skip_ws(rest), %{})
  defp parse_value(<<"[", rest::binary>>), do: parse_array(skip_ws(rest), [])
  defp parse_value(<<"\"", rest::binary>>), do: parse_string(rest, [])
  defp parse_value(<<"true", rest::binary>>), do: {true, rest}
  defp parse_value(<<"false", rest::binary>>), do: {false, rest}
  defp parse_value(<<"null", rest::binary>>), do: {nil, rest}
  defp parse_value(binary), do: parse_number(binary)

  defp parse_object(<<"}", rest::binary>>, acc), do: {acc, rest}

  defp parse_object(<<"\"", rest::binary>>, acc) do
    {key, after_key} = parse_string(rest, [])
    <<":", after_colon::binary>> = skip_ws(after_key)
    {value, after_value} = parse_value(skip_ws(after_colon))
    acc = Map.put(acc, key, value)

    case skip_ws(after_value) do
      <<",", more::binary>> -> parse_object(skip_ws(more), acc)
      <<"}", more::binary>> -> {acc, more}
    end
  end

  defp parse_array(<<"]", rest::binary>>, acc), do: {Enum.reverse(acc), rest}

  defp parse_array(binary, acc) do
    {value, rest} = parse_value(binary)
    acc = [value | acc]

    case skip_ws(rest) do
      <<",", more::binary>> -> parse_array(skip_ws(more), acc)
      <<"]", more::binary>> -> {Enum.reverse(acc), more}
    end
  end

  defp parse_string(<<"\"", rest::binary>>, acc) do
    {acc |> Enum.reverse() |> IO.iodata_to_binary(), rest}
  end

  defp parse_string(<<"\\", c, rest::binary>>, acc) do
    parse_string(rest, [unescape(c) | acc])
  end

  defp parse_string(<<c::utf8, rest::binary>>, acc) do
    parse_string(rest, [<<c::utf8>> | acc])
  end

  defp unescape(?n), do: "\n"
  defp unescape(?r), do: "\r"
  defp unescape(?t), do: "\t"
  defp unescape(?"), do: "\""
  defp unescape(?\\), do: "\\"
  defp unescape(?/), do: "/"
  defp unescape(c), do: <<c>>

  defp parse_number(binary) do
    {number, rest} = take_number(binary, [])
    text = IO.iodata_to_binary(number)

    value =
      if String.contains?(text, ".") or String.contains?(text, "e") or String.contains?(text, "E") do
        String.to_float(text)
      else
        String.to_integer(text)
      end

    {value, rest}
  end

  defp take_number(<<c, rest::binary>>, acc)
       when c in ?0..?9 or c in [?-, ?+, ?., ?e, ?E] do
    take_number(rest, [<<c>> | acc])
  end

  defp take_number(binary, acc), do: {Enum.reverse(acc), binary}
end
