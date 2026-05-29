# frozen_string_literal: true

require "json"
require "net/http"
require "securerandom"
require "uri"

require_relative "errors"
require_relative "version"

module Ubag
  API_VERSION = "2026-05-22"
  SDK_NAME = "ubag-ruby"
  SDK_VERSION = VERSION
  JSON_CONTENT_TYPE = "application/json"
  CROCKFORD_BASE32 = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

  # Generates a ULID-style idempotency key: 26 Crockford base32 characters.
  def self.generate_idempotency_key(now_ms = (Time.now.to_f * 1000).to_i)
    timestamp = encode_base32(now_ms.negative? ? 0 : now_ms, 10)
    entropy = Array.new(16) { CROCKFORD_BASE32[SecureRandom.random_number(32)] }.join
    timestamp + entropy
  end

  def self.encode_base32(value, length)
    buffer = Array.new(length, "0")
    remaining = value
    (length - 1).downto(0) do |index|
      buffer[index] = CROCKFORD_BASE32[remaining % 32]
      remaining /= 32
    end
    buffer.join
  end

  # Default transport backed by Net::HTTP.
  class NetHttpTransport
    def call(method:, url:, headers:, body:)
      uri = URI.parse(url)
      request_class = Net::HTTP.const_get(method.capitalize)
      request = request_class.new(uri)
      headers.each { |key, value| request[key] = value }
      request.body = body unless body.nil?

      response = Net::HTTP.start(uri.hostname, uri.port, use_ssl: uri.scheme == "https") do |http|
        http.request(request)
      end

      response_headers = {}
      response.each_header { |key, value| response_headers[key] = value }
      { status: response.code.to_i, headers: response_headers, body: response.body || "" }
    end
  end

  # Ruby client for the UBAG v0 REST gateway.
  class Client
    def initialize(base_url, app_secret: nil, api_version: API_VERSION, transport: nil, headers: {})
      raise ArgumentError, "base_url is required" if base_url.nil? || base_url.strip.empty?
      raise ArgumentError, "base_url must include scheme and host" unless base_url.include?("://")

      @base_url = base_url.sub(%r{/+\z}, "")
      @app_secret = app_secret
      @api_version = api_version
      @transport = transport || NetHttpTransport.new
      @default_headers = headers
    end

    # --- System ---------------------------------------------------------

    def health(**options)
      request_json("GET", "/v1/health", nil, options)
    end

    def ready(**options)
      request_json("GET", "/v1/ready", nil, options)
    end

    def version(**options)
      options.delete(:idempotency_key)
      request_json("GET", "/v1/version", nil, options)
    end

    def metrics(**options)
      options = inject_header(options, "Accept", "text/plain")
      send_request("GET", "/v1/metrics", nil, nil, options)[:body]
    end

    # --- Jobs -----------------------------------------------------------

    def create_job(request, **options)
      body = deep_dup(request)
      api_version = string_field(body, "api_version") || options[:api_version] || @api_version
      idempotency_key = string_field(body, "idempotency_key") || options[:idempotency_key] || Ubag.generate_idempotency_key

      body["api_version"] = api_version
      body["idempotency_key"] = idempotency_key
      ensure_sdk_metadata(body)

      options = options.merge(api_version: api_version, idempotency_key: idempotency_key)
      request_json("POST", "/v1/jobs", body, options)
    end

    def get_job(job_id, **options)
      request_json("GET", "/v1/jobs/#{encode(job_id)}", nil, options)
    end

    def list_jobs(cursor: nil, limit: nil, status: nil, target: nil, sort: nil, fields: nil, include: nil, **options)
      pairs = []
      add_pair(pairs, "cursor", cursor)
      add_pair(pairs, "limit", limit)
      add_pair(pairs, "filter[status]", status)
      add_pair(pairs, "filter[target]", target)
      add_pair(pairs, "sort", sort)
      add_pair(pairs, "fields", fields&.join(","))
      add_pair(pairs, "include", include&.join(","))
      request_json("GET", "/v1/jobs#{encode_query(pairs)}", nil, options)
    end

    def cancel_job(job_id, request = {}, **options)
      mutate("/v1/jobs/#{encode(job_id)}/cancel", request, options)
    end

    def retry_job(job_id, request = {}, **options)
      mutate("/v1/jobs/#{encode(job_id)}/retry", request, options)
    end

    # --- Job events -----------------------------------------------------

    def list_job_events(job_id, cursor: nil, after_sequence: nil, limit: nil, **options)
      pairs = []
      add_pair(pairs, "cursor", cursor)
      add_pair(pairs, "after_sequence", after_sequence)
      add_pair(pairs, "limit", limit)
      request_json("GET", "/v1/jobs/#{encode(job_id)}/events#{encode_query(pairs)}", nil, options)
    end

    def stream_job_events_sse(job_id, **options)
      options = inject_header(options, "Accept", "text/event-stream")
      send_request("GET", "/v1/sse/jobs/#{encode(job_id)}", nil, nil, options)[:body]
    end

    # --- Artifacts ------------------------------------------------------

    def list_job_artifacts(job_id, **options)
      request_json("GET", "/v1/jobs/#{encode(job_id)}/artifacts", nil, options)
    end

    def get_job_artifact(job_id, key, **options)
      response = send_request("GET", "/v1/jobs/#{encode(job_id)}/artifacts/#{encode(key)}", nil, nil, options)
      {
        body: response[:body],
        content_type: header_value(response[:headers], "content-type"),
        checksum: header_value(response[:headers], "ubag-artifact-checksum")
      }
    end

    def put_job_artifact(job_id, key, body, content_type: "application/octet-stream", **options)
      options[:idempotency_key] ||= Ubag.generate_idempotency_key
      resolved_type = content_type.nil? || content_type.empty? ? "application/octet-stream" : content_type
      response = send_request("PUT", "/v1/jobs/#{encode(job_id)}/artifacts/#{encode(key)}", body, resolved_type, options)
      decode_json(response[:body])
    end

    def delete_job_artifact(job_id, key, **options)
      options[:idempotency_key] ||= Ubag.generate_idempotency_key
      request_json("DELETE", "/v1/jobs/#{encode(job_id)}/artifacts/#{encode(key)}", nil, options)
      nil
    end

    # --- Operator collections ------------------------------------------

    def list_workflows(**options)
      request_json("GET", "/v1/workflows", nil, options)
    end

    def list_templates(**options)
      request_json("GET", "/v1/templates", nil, options)
    end

    def list_targets(cursor: nil, limit: nil, **options)
      request_json("GET", "/v1/targets#{build_list_query(cursor, limit)}", nil, options)
    end

    def list_adapters(cursor: nil, limit: nil, **options)
      request_json("GET", "/v1/adapters#{build_list_query(cursor, limit)}", nil, options)
    end

    def list_apps(cursor: nil, limit: nil, **options)
      request_json("GET", "/v1/apps#{build_list_query(cursor, limit)}", nil, options)
    end

    def list_devices(cursor: nil, limit: nil, **options)
      request_json("GET", "/v1/devices#{build_list_query(cursor, limit)}", nil, options)
    end

    def list_webhooks(cursor: nil, limit: nil, **options)
      request_json("GET", "/v1/webhooks#{build_list_query(cursor, limit)}", nil, options)
    end

    def list_audit_events(cursor: nil, limit: nil, **options)
      request_json("GET", "/v1/audit#{build_list_query(cursor, limit)}", nil, options)
    end

    def list_events(cursor: nil, limit: nil, **options)
      request_json("GET", "/v1/events#{build_list_query(cursor, limit)}", nil, options)
    end

    # --- Webhook replay & cache ----------------------------------------

    def replay_webhook_delivery(request = {}, **options)
      mutate("/v1/webhooks/replay", request, options)
    end

    def cache_status(**options)
      request_json("GET", "/v1/cache", nil, options)
    end

    private

    def mutate(path, request, options)
      body = deep_dup(request)
      api_version = string_field(body, "api_version") || options[:api_version] || @api_version
      idempotency_key = string_field(body, "idempotency_key") || options[:idempotency_key] || Ubag.generate_idempotency_key

      body["api_version"] = api_version
      body["idempotency_key"] = idempotency_key
      options = options.merge(api_version: api_version, idempotency_key: idempotency_key)
      request_json("POST", path, body, options)
    end

    def request_json(method, path, body, options)
      serialized = body.nil? ? nil : JSON.generate(body)
      content_type = serialized.nil? ? nil : JSON_CONTENT_TYPE
      response = send_request(method, path, serialized, content_type, options)
      return {} if response[:body].nil? || response[:body].empty? || response[:status] == 204

      decode_json(response[:body])
    end

    def send_request(method, path, body, content_type, options)
      url = @base_url + path
      api_version = options[:api_version] || @api_version

      headers = {
        "Accept" => JSON_CONTENT_TYPE,
        "Ubag-Api-Version" => api_version,
        "Ubag-Sdk-Name" => SDK_NAME,
        "Ubag-Sdk-Version" => SDK_VERSION
      }
      headers.merge!(@default_headers)
      (options[:headers] || {}).each { |key, value| headers[key] = value }
      headers["Authorization"] = "Bearer #{@app_secret}" if @app_secret && !headers.key?("Authorization")
      headers["Idempotency-Key"] = options[:idempotency_key] if options[:idempotency_key]
      headers["Content-Type"] = content_type || JSON_CONTENT_TYPE unless body.nil?

      begin
        response = @transport.call(method: method, url: url, headers: headers, body: body)
      rescue StandardError => e
        raise TransportError.new(method, url, e.message)
      end

      if response[:status] < 200 || response[:status] >= 300
        envelope = parse_envelope(response[:body])
        raise ApiError.new(
          status: response[:status],
          method: method,
          url: url,
          headers: response[:headers] || {},
          raw_body: response[:body],
          envelope: envelope
        )
      end

      response
    end

    def parse_envelope(body)
      return nil if body.nil? || body.empty?

      parsed = JSON.parse(body)
      Ubag.error_envelope?(parsed) ? parsed : nil
    rescue JSON::ParserError
      nil
    end

    def decode_json(body)
      JSON.parse(body)
    end

    def ensure_sdk_metadata(body)
      client = body["client"]
      unless client.is_a?(Hash)
        client = {}
        body["client"] = client
      end
      client["sdk"] ||= { "name" => SDK_NAME, "version" => SDK_VERSION }
    end

    def string_field(body, key)
      value = body[key]
      value.is_a?(String) && !value.empty? ? value : nil
    end

    def header_value(headers, name)
      headers.each do |key, value|
        return value if key.to_s.downcase == name
      end
      ""
    end

    def inject_header(options, key, value)
      headers = (options[:headers] || {}).merge(key => value)
      options.merge(headers: headers)
    end

    def add_pair(pairs, key, value)
      return if value.nil? || value.to_s.empty?
      return if value.is_a?(Integer) && value <= 0

      pairs << [key, value.to_s]
    end

    def build_list_query(cursor, limit)
      pairs = []
      add_pair(pairs, "cursor", cursor)
      add_pair(pairs, "limit", limit)
      encode_query(pairs)
    end

    def encode_query(pairs)
      return "" if pairs.empty?

      "?" + pairs.map { |key, value| "#{encode(key)}=#{encode(value)}" }.join("&")
    end

    def encode(value)
      URI.encode_www_form_component(value.to_s)
    end

    def deep_dup(value)
      case value
      when Hash
        value.each_with_object({}) { |(key, val), acc| acc[key.to_s] = deep_dup(val) }
      when Array
        value.map { |item| deep_dup(item) }
      else
        value
      end
    end
  end
end
