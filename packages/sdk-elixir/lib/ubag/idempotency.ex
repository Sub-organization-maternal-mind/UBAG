defmodule Ubag.Idempotency do
  @moduledoc "Generates ULID-style idempotency keys: 26 Crockford base32 characters."

  @alphabet ~c"0123456789ABCDEFGHJKMNPQRSTVWXYZ"

  @spec generate(integer()) :: binary()
  def generate(now_millis \\ System.system_time(:millisecond)) do
    timestamp = encode_base32(max(now_millis, 0), 10)

    random =
      for <<byte <- :crypto.strong_rand_bytes(16)>>, into: "" do
        <<Enum.at(@alphabet, Bitwise.band(byte, 31))>>
      end

    timestamp <> random
  end

  defp encode_base32(value, length) do
    {chars, _} =
      Enum.reduce(1..length, {[], value}, fn _, {acc, remaining} ->
        char = Enum.at(@alphabet, rem(remaining, 32))
        {[char | acc], div(remaining, 32)}
      end)

    chars |> List.to_string()
  end
end
