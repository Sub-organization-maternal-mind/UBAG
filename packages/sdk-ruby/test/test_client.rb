# frozen_string_literal: true

require "json"
require "minitest/autorun"

require_relative "../lib/ubag"

# Capturing transport that records the last request and returns a canned response.
class CapturingTransport
  attr_reader :last_request

  def initialize(status:, body:, headers: {})
    @status = status
    @body = body
    @headers = headers
  end

  def call(method:, url:, headers:, body:)
    @last_request = { method: method, url: url, headers: headers, body: body }
    { status: @status, headers: @headers, body: @body }
  end
end

class UbagClientTest < Minitest::Test
  def build_client(transport)
    Ubag::Client.new("http://127.0.0.1:7878/", app_secret: "app_secret_fixture", transport: transport)
  end

  def test_health_sends_version_and_auth_headers
    transport = CapturingTransport.new(status: 200, body: '{"status":"ok"}')
    result = build_client(transport).health

    assert_equal "ok", result["status"]
    request = transport.last_request
    assert_equal "GET", request[:method]
    assert_equal "http://127.0.0.1:7878/v1/health", request[:url]
    assert_equal Ubag::API_VERSION, request[:headers]["Ubag-Api-Version"]
    assert_equal Ubag::SDK_NAME, request[:headers]["Ubag-Sdk-Name"]
    assert_equal "Bearer app_secret_fixture", request[:headers]["Authorization"]
    assert_nil request[:body]
  end

  def test_version_omits_idempotency_key
    transport = CapturingTransport.new(status: 200, body: '{"version":"0.0.0"}')
    build_client(transport).version(idempotency_key: "ignored")

    request = transport.last_request
    assert_equal "http://127.0.0.1:7878/v1/version", request[:url]
    refute request[:headers].key?("Idempotency-Key")
  end

  def test_create_job_injects_version_idempotency_and_sdk_metadata
    transport = CapturingTransport.new(status: 202, body: '{"status":"queued"}')
    body = {
      "client" => { "app_id" => "fixture-app", "app_version" => "0.0.0" },
      "job" => { "target" => "mock_target", "command_type" => "echo" }
    }

    build_client(transport).create_job(body, idempotency_key: "idem_ruby_sdk")

    request = transport.last_request
    assert_equal "POST", request[:method]
    assert_equal "http://127.0.0.1:7878/v1/jobs", request[:url]
    assert_equal "idem_ruby_sdk", request[:headers]["Idempotency-Key"]
    assert_equal "application/json", request[:headers]["Content-Type"]

    sent = JSON.parse(request[:body])
    assert_equal Ubag::API_VERSION, sent["api_version"]
    assert_equal "idem_ruby_sdk", sent["idempotency_key"]
    assert_equal Ubag::SDK_NAME, sent.dig("client", "sdk", "name")
    assert_equal Ubag::SDK_VERSION, sent.dig("client", "sdk", "version")
  end

  def test_create_job_generates_idempotency_key_when_missing
    transport = CapturingTransport.new(status: 202, body: '{"status":"queued"}')
    build_client(transport).create_job({ "job" => { "target" => "mock_target" } })

    request = transport.last_request
    key = request[:headers]["Idempotency-Key"]
    assert_equal 26, key.length
    assert_equal key, JSON.parse(request[:body])["idempotency_key"]
  end

  def test_list_jobs_builds_filter_query
    transport = CapturingTransport.new(status: 200, body: '{"jobs":[]}')
    build_client(transport).list_jobs(cursor: "cursor_1", limit: 1, status: "completed")

    assert_equal(
      "http://127.0.0.1:7878/v1/jobs?cursor=cursor_1&limit=1&filter%5Bstatus%5D=completed",
      transport.last_request[:url]
    )
  end

  def test_cancel_job_is_idempotent_post
    transport = CapturingTransport.new(status: 202, body: '{"status":"cancelled"}')
    build_client(transport).cancel_job("job_1", { "reason" => "caller_cancelled" }, idempotency_key: "idem_cancel")

    request = transport.last_request
    assert_equal "POST", request[:method]
    assert_equal "http://127.0.0.1:7878/v1/jobs/job_1/cancel", request[:url]
    assert_equal "idem_cancel", request[:headers]["Idempotency-Key"]
    sent = JSON.parse(request[:body])
    assert_equal "idem_cancel", sent["idempotency_key"]
    assert_equal "caller_cancelled", sent["reason"]
  end

  def test_put_artifact_sends_bytes_and_generates_key
    transport = CapturingTransport.new(status: 201, body: '{"idempotent_replay":false}')
    build_client(transport).put_job_artifact("job_1", "report.txt", "hello artifact", content_type: "text/plain")

    request = transport.last_request
    assert_equal "PUT", request[:method]
    assert_equal "http://127.0.0.1:7878/v1/jobs/job_1/artifacts/report.txt", request[:url]
    assert_equal "text/plain", request[:headers]["Content-Type"]
    assert_equal 26, request[:headers]["Idempotency-Key"].length
    assert_equal "hello artifact", request[:body]
  end

  def test_get_artifact_returns_bytes_and_checksum
    transport = CapturingTransport.new(
      status: 200,
      body: "hello artifact",
      headers: { "content-type" => "text/plain", "ubag-artifact-checksum" => "sha256_fixture" }
    )
    download = build_client(transport).get_job_artifact("job_1", "report.txt")

    assert_equal "hello artifact", download[:body]
    assert_equal "text/plain", download[:content_type]
    assert_equal "sha256_fixture", download[:checksum]
  end

  def test_metrics_request_sets_text_accept
    transport = CapturingTransport.new(status: 200, body: "ubag_gateway_requests_total 1\n")
    text = build_client(transport).metrics

    assert_equal "ubag_gateway_requests_total 1\n", text
    assert_equal "text/plain", transport.last_request[:headers]["Accept"]
  end

  def test_api_error_envelope_is_parsed
    transport = CapturingTransport.new(
      status: 401,
      body: JSON.generate(
        "error" => {
          "code" => "UBAG-AUTH-MISSING-001",
          "category" => "auth",
          "message" => "No supported credential was provided",
          "retryable" => false,
          "trace_id" => "trace_auth_missing"
        }
      )
    )

    error = assert_raises(Ubag::ApiError) { build_client(transport).list_workflows }
    assert_equal 401, error.status
    assert_equal "UBAG-AUTH-MISSING-001", error.code
    assert_equal "auth", error.category
    refute error.retryable?
    assert_equal "trace_auth_missing", error.trace_id
    assert_includes error.message, "No supported credential"
  end
end
