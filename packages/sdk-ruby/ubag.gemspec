# frozen_string_literal: true

require_relative "lib/ubag/version"

Gem::Specification.new do |spec|
  spec.name = "ubag"
  spec.version = Ubag::VERSION
  spec.authors = ["UBAG"]
  spec.summary = "Ruby client for the UBAG v0 REST gateway."
  spec.description = "Idiomatic Ruby SDK for the UBAG gateway with stable errors and idempotency."
  spec.homepage = "https://github.com/ubag/ubag"
  spec.license = "Apache-2.0"
  spec.required_ruby_version = ">= 3.0"

  spec.files = Dir["lib/**/*.rb", "README.md"]
  spec.require_paths = ["lib"]

  # Runtime dependencies are intentionally empty: the SDK uses only the Ruby
  # standard library (net/http, json, securerandom).
  spec.add_development_dependency "minitest", "~> 5.0"
  spec.add_development_dependency "rake", "~> 13.0"
end
