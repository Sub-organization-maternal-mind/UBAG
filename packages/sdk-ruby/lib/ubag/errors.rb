# frozen_string_literal: true

module Ubag
  # Base class for all SDK errors.
  class Error < StandardError; end

  # Raised when a request could not be sent (network/transport failure).
  class TransportError < Error
    attr_reader :method, :url

    def initialize(method, url, cause)
      @method = method
      @url = url
      super("UBAG API request could not be sent: #{method} #{url}: #{cause}")
    end
  end

  # Raised when the gateway returns a non-2xx response.
  class ApiError < Error
    attr_reader :status, :method, :url, :headers, :raw_body, :envelope

    def initialize(status:, method:, url:, headers:, raw_body:, envelope:)
      @status = status
      @method = method
      @url = url
      @headers = headers
      @raw_body = raw_body
      @envelope = envelope
      super(message_for(status, envelope))
    end

    def code
      dig_error("code")
    end

    def category
      dig_error("category")
    end

    def retryable?
      dig_error("retryable") == true
    end

    def trace_id
      value = dig_error("trace_id")
      return value if value.is_a?(String) && !value.empty?

      header_value("ubag-trace-id") || header_value("x-request-id")
    end

    private

    def message_for(status, envelope)
      message = envelope&.dig("error", "message")
      return message if message.is_a?(String) && !message.empty?

      "UBAG API request failed with HTTP #{status}"
    end

    def dig_error(key)
      envelope&.dig("error", key)
    end

    def header_value(name)
      headers.each do |key, value|
        return value if key.to_s.downcase == name
      end
      nil
    end
  end

  # Returns true when a parsed body looks like a genuine UBAG error envelope.
  def self.error_envelope?(body)
    return false unless body.is_a?(Hash)

    error = body["error"]
    error.is_a?(Hash) && error["code"].is_a?(String) && error["code"].start_with?("UBAG-")
  end
end
