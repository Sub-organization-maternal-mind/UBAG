defmodule Ubag.MixProject do
  use Mix.Project

  def project do
    [
      app: :ubag,
      version: "0.0.0",
      elixir: "~> 1.14",
      start_permanent: Mix.env() == :prod,
      description: "Elixir client for the UBAG v0 REST gateway with stable errors and idempotency.",
      deps: deps(),
      package: package()
    ]
  end

  def application do
    [extra_applications: [:logger, :inets, :ssl, :crypto]]
  end

  # No runtime dependencies: the default transport uses the built-in :httpc
  # client and a minimal JSON codec (Ubag.JSON). Req/Finch can be plugged in
  # via the Ubag.Transport behaviour without adding a dependency here.
  defp deps, do: []

  defp package do
    [
      licenses: ["Apache-2.0"],
      links: %{}
    ]
  end
end
