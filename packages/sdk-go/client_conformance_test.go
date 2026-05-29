package ubag

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type fixtureFile struct {
	Suite     string            `json:"suite"`
	Scenarios []fixtureScenario `json:"scenarios"`
}

type fixtureScenario struct {
	ID       string          `json:"id"`
	Category string          `json:"category"`
	Title    string          `json:"title"`
	Request  fixtureRequest  `json:"request"`
	Response fixtureResponse `json:"response"`
	Expect   map[string]any  `json:"expect"`
}

type fixtureRequest struct {
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	Headers  map[string]string `json:"headers"`
	Body     JSON              `json:"body"`
	BodyText string            `json:"body_text"`
}

type fixtureResponse struct {
	Status   int               `json:"status"`
	Headers  map[string]string `json:"headers"`
	Body     JSON              `json:"body"`
	BodyText string            `json:"body_text"`
}

type recordedRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
}

func TestSharedConformanceFixtures(t *testing.T) {
	fixture := loadFixture(t)
	if fixture.Suite != "ubag.v0.sdk.baseline" {
		t.Fatalf("suite = %q", fixture.Suite)
	}

	for _, scenario := range fixture.Scenarios {
		t.Run(scenario.ID, func(t *testing.T) {
			var recorded recordedRequest
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Fatalf("read request body: %v", err)
				}
				recorded = recordedRequest{
					Method:  request.Method,
					Path:    request.URL.RequestURI(),
					Headers: request.Header.Clone(),
					Body:    body,
				}

				for key, value := range scenario.Response.Headers {
					writer.Header().Set(key, value)
				}
				writer.WriteHeader(scenario.Response.Status)
				if scenario.Response.Body != nil {
					if err := json.NewEncoder(writer).Encode(scenario.Response.Body); err != nil {
						t.Fatalf("encode fixture response: %v", err)
					}
				} else if scenario.Response.BodyText != "" {
					_, _ = writer.Write([]byte(scenario.Response.BodyText))
				}
			}))
			defer server.Close()

			client := newFixtureClient(t, server.URL, scenario)
			result, err := invokeScenario(t, client, scenario)

			if expectedThrow, ok := scenario.Expect["throws"].(string); ok {
				assertAPIErrorExpectations(t, err, expectedThrow, scenario.Expect)
			} else {
				if err != nil {
					t.Fatalf("unexpected SDK error: %v", err)
				}
				assertBodyExpectations(t, result, scenario.Expect)
			}

			assertRecordedRequest(t, scenario.Request, recorded)
		})
	}
}

func TestCreateJobAddsGoSDKMetadataWhenMissing(t *testing.T) {
	var recordedBody JSON
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if err := json.NewDecoder(request.Body).Decode(&recordedBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if request.Header.Get("Idempotency-Key") != "idem_go_sdk" {
			t.Fatalf("idempotency header = %q", request.Header.Get("Idempotency-Key"))
		}
		writer.WriteHeader(http.StatusAccepted)
		_, _ = writer.Write([]byte(`{"api_version":"2026-05-22","job_id":"job_fixture","idempotent_replay":false,"status":"queued","target":"mock_target","trace_id":"trace_fixture"}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, WithAppSecret("app_secret_fixture"))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	_, err = client.CreateJob(
		context.Background(),
		JSON{
			"client": JSON{"app_id": "fixture-app", "app_version": "0.0.0"},
			"job": JSON{
				"target":       "mock_target",
				"command_type": "echo",
				"input":        JSON{"prompt": "Hello UBAG"},
			},
		},
		WithIdempotencyKey("idem_go_sdk"),
	)
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	clientMetadata := recordedBody["client"].(map[string]any)
	sdkMetadata := clientMetadata["sdk"].(map[string]any)
	if sdkMetadata["name"] != SDKName {
		t.Fatalf("sdk name = %v, want %s", sdkMetadata["name"], SDKName)
	}
	if sdkMetadata["version"] != SDKVersion {
		t.Fatalf("sdk version = %v, want %s", sdkMetadata["version"], SDKVersion)
	}
	if recordedBody["idempotency_key"] != "idem_go_sdk" {
		t.Fatalf("idempotency_key = %v", recordedBody["idempotency_key"])
	}
}

func loadFixture(t *testing.T) fixtureFile {
	t.Helper()
	path := filepath.Join("..", "conformance", "fixtures", "v0", "scenarios.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var fixture fixtureFile
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return fixture
}

func findScenario(t *testing.T, fixture fixtureFile, id string) fixtureScenario {
	t.Helper()
	for _, scenario := range fixture.Scenarios {
		if scenario.ID == id {
			return scenario
		}
	}
	t.Fatalf("scenario %s not found", id)
	return fixtureScenario{}
}

func newFixtureClient(t *testing.T, baseURL string, scenario fixtureScenario) *Client {
	t.Helper()
	options := []Option{}
	if authorization := scenario.Request.Headers["Authorization"]; strings.HasPrefix(authorization, "Bearer ") {
		options = append(options, WithAppSecret(strings.TrimPrefix(authorization, "Bearer ")))
	}

	client, err := NewClient(baseURL, options...)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

func invokeScenario(t *testing.T, client *Client, scenario fixtureScenario) (JSON, error) {
	t.Helper()
	request := scenario.Request
	parsed, err := url.Parse(request.Path)
	if err != nil {
		t.Fatalf("parse fixture path: %v", err)
	}

	options := requestOptionsFromHeaders(request.Headers)
	ctx := context.Background()

	switch {
	case request.Method == http.MethodGet && parsed.Path == "/v1/health":
		return client.Health(ctx, options...)
	case request.Method == http.MethodGet && parsed.Path == "/v1/ready":
		return client.Ready(ctx, options...)
	case request.Method == http.MethodGet && parsed.Path == "/v1/version":
		return client.Version(ctx)
	case request.Method == http.MethodGet && parsed.Path == "/v1/workflows":
		return client.ListWorkflows(ctx, options...)
	case request.Method == http.MethodGet && parsed.Path == "/v1/templates":
		return client.ListTemplates(ctx, options...)
	case request.Method == http.MethodGet && parsed.Path == "/v1/targets":
		return client.ListTargets(ctx, listParamsFromQuery(parsed.Query()), options...)
	case request.Method == http.MethodGet && parsed.Path == "/v1/adapters":
		return client.ListAdapters(ctx, listParamsFromQuery(parsed.Query()), options...)
	case request.Method == http.MethodGet && parsed.Path == "/v1/apps":
		return client.ListApps(ctx, listParamsFromQuery(parsed.Query()), options...)
	case request.Method == http.MethodGet && parsed.Path == "/v1/devices":
		return client.ListDevices(ctx, listParamsFromQuery(parsed.Query()), options...)
	case request.Method == http.MethodGet && parsed.Path == "/v1/audit":
		return client.ListAuditEvents(ctx, listParamsFromQuery(parsed.Query()), options...)
	case request.Method == http.MethodGet && parsed.Path == "/v1/webhooks":
		return client.ListWebhooks(ctx, listParamsFromQuery(parsed.Query()), options...)
	case request.Method == http.MethodGet && parsed.Path == "/v1/events":
		return client.ListEvents(ctx, listParamsFromQuery(parsed.Query()), options...)
	case request.Method == http.MethodGet && parsed.Path == "/v1/cache":
		return client.CacheStatus(ctx, options...)
	case request.Method == http.MethodGet && parsed.Path == "/v1/metrics":
		body, err := client.GetMetrics(ctx, options...)
		return JSON{"body": body}, err
	case request.Method == http.MethodGet && parsed.Path == "/v1/jobs":
		return client.ListJobs(ctx, listJobsParamsFromQuery(parsed.Query()), options...)
	case request.Method == http.MethodGet && strings.HasPrefix(parsed.Path, "/v1/jobs/") && strings.HasSuffix(parsed.Path, "/events"):
		jobID := strings.TrimSuffix(strings.TrimPrefix(parsed.Path, "/v1/jobs/"), "/events")
		return client.ListJobEvents(ctx, jobID, listJobEventsParamsFromQuery(parsed.Query()), options...)
	case request.Method == http.MethodGet && strings.HasPrefix(parsed.Path, "/v1/jobs/") && strings.HasSuffix(parsed.Path, "/artifacts"):
		jobID := strings.TrimSuffix(strings.TrimPrefix(parsed.Path, "/v1/jobs/"), "/artifacts")
		return client.ListJobArtifacts(ctx, jobID, options...)
	case request.Method == http.MethodGet && strings.HasPrefix(parsed.Path, "/v1/sse/jobs/"):
		body, err := client.StreamJobEventsSSE(ctx, strings.TrimPrefix(parsed.Path, "/v1/sse/jobs/"), options...)
		return JSON{"body": string(body)}, err
	case request.Method == http.MethodGet && strings.HasPrefix(parsed.Path, "/v1/jobs/") && strings.Contains(parsed.Path, "/artifacts/"):
		jobID, key := splitArtifactPath(t, parsed.Path)
		artifact, err := client.GetJobArtifact(ctx, jobID, key, options...)
		if err != nil {
			return nil, err
		}
		return JSON{
			"body":         string(artifact.Body),
			"content_type": artifact.ContentType,
			"checksum":     artifact.Checksum,
		}, nil
	case request.Method == http.MethodPut && strings.HasPrefix(parsed.Path, "/v1/jobs/") && strings.Contains(parsed.Path, "/artifacts/"):
		jobID, key := splitArtifactPath(t, parsed.Path)
		return client.PutJobArtifact(ctx, jobID, key, []byte(request.BodyText), request.Headers["Content-Type"], options...)
	case request.Method == http.MethodDelete && strings.HasPrefix(parsed.Path, "/v1/jobs/") && strings.Contains(parsed.Path, "/artifacts/"):
		jobID, key := splitArtifactPath(t, parsed.Path)
		return nil, client.DeleteJobArtifact(ctx, jobID, key, options...)
	case request.Method == http.MethodGet && strings.HasPrefix(parsed.Path, "/v1/jobs/"):
		return client.GetJob(ctx, strings.TrimPrefix(parsed.Path, "/v1/jobs/"), options...)
	case request.Method == http.MethodPost && parsed.Path == "/v1/jobs":
		return client.CreateJob(ctx, resolveSDKPlaceholders(request.Body), options...)
	case request.Method == http.MethodPost && strings.HasSuffix(parsed.Path, "/cancel"):
		jobID := strings.TrimSuffix(strings.TrimPrefix(parsed.Path, "/v1/jobs/"), "/cancel")
		return client.CancelJob(ctx, jobID, request.Body, options...)
	case request.Method == http.MethodPost && strings.HasSuffix(parsed.Path, "/retry"):
		jobID := strings.TrimSuffix(strings.TrimPrefix(parsed.Path, "/v1/jobs/"), "/retry")
		return client.RetryJob(ctx, jobID, request.Body, options...)
	case request.Method == http.MethodPost && parsed.Path == "/v1/webhooks/replay":
		return client.ReplayWebhookDelivery(ctx, request.Body, options...)
	default:
		t.Fatalf("no SDK mapping for %s %s", request.Method, request.Path)
	}

	return nil, nil
}

func splitArtifactPath(t *testing.T, path string) (string, string) {
	t.Helper()
	body := strings.TrimPrefix(path, "/v1/jobs/")
	parts := strings.SplitN(body, "/artifacts/", 2)
	if len(parts) != 2 {
		t.Fatalf("invalid artifact route: %s", path)
	}
	jobID, err := url.PathUnescape(parts[0])
	if err != nil {
		t.Fatalf("decode artifact job id: %v", err)
	}
	key, err := url.PathUnescape(parts[1])
	if err != nil {
		t.Fatalf("decode artifact key: %v", err)
	}
	return jobID, key
}

func requestOptionsFromHeaders(headers map[string]string) []RequestOption {
	options := []RequestOption{}
	if apiVersion := headers["Ubag-Api-Version"]; apiVersion != "" {
		options = append(options, WithRequestAPIVersion(apiVersion))
	}
	if idempotencyKey := headers["Idempotency-Key"]; idempotencyKey != "" {
		options = append(options, WithIdempotencyKey(idempotencyKey))
	}
	return options
}

func listJobsParamsFromQuery(query url.Values) ListJobsParams {
	limit, _ := strconv.Atoi(query.Get("limit"))
	return ListJobsParams{
		Cursor: query.Get("cursor"),
		Limit:  limit,
		Status: query.Get("filter[status]"),
		Target: query.Get("filter[target]"),
		Sort:   query.Get("sort"),
	}
}

func listParamsFromQuery(query url.Values) ListParams {
	limit, _ := strconv.Atoi(query.Get("limit"))
	return ListParams{
		Cursor: query.Get("cursor"),
		Limit:  limit,
	}
}

func listJobEventsParamsFromQuery(query url.Values) ListJobEventsParams {
	limit, _ := strconv.Atoi(query.Get("limit"))
	afterSequence, _ := strconv.Atoi(query.Get("after_sequence"))
	return ListJobEventsParams{
		Cursor:        query.Get("cursor"),
		AfterSequence: afterSequence,
		Limit:         limit,
	}
}

func assertRecordedRequest(t *testing.T, expected fixtureRequest, recorded recordedRequest) {
	t.Helper()
	if recorded.Method != expected.Method {
		t.Fatalf("method = %s, want %s", recorded.Method, expected.Method)
	}
	if recorded.Path != expected.Path {
		t.Fatalf("path = %s, want %s", recorded.Path, expected.Path)
	}
	for key, value := range expected.Headers {
		if got := recorded.Headers.Get(key); got != value {
			t.Fatalf("header %s = %q, want %q", key, got, value)
		}
	}
	if expected.Body != nil {
		var actual any
		if err := json.NewDecoder(bytes.NewReader(recorded.Body)).Decode(&actual); err != nil {
			t.Fatalf("decode recorded body: %v", err)
		}
		expectedBody := resolveSDKPlaceholders(expected.Body)
		if !reflect.DeepEqual(actual, map[string]any(expectedBody)) {
			t.Fatalf("body = %#v, want %#v", actual, expectedBody)
		}
	}
	if expected.BodyText != "" && string(recorded.Body) != expected.BodyText {
		t.Fatalf("body text = %q, want %q", string(recorded.Body), expected.BodyText)
	}
}

func assertBodyExpectations(t *testing.T, body JSON, expect map[string]any) {
	t.Helper()
	if okValue, exists := expect["ok"]; exists && okValue != true {
		t.Fatalf("expect ok = %v", okValue)
	}
	for key, want := range expect {
		if !strings.HasPrefix(key, "body.") {
			continue
		}
		got, ok := valueAtPath(body, strings.TrimPrefix(key, "body."))
		if !ok {
			t.Fatalf("missing path %s in %#v", key, body)
		}
		if !fixtureEqual(got, want) {
			t.Fatalf("%s = %#v, want %#v", key, got, want)
		}
	}
}

func assertAPIErrorExpectations(t *testing.T, err error, expectedThrow string, expect map[string]any) {
	t.Helper()
	if err == nil {
		t.Fatal("expected SDK error, got nil")
	}

	var apiError *APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("error = %T, want *APIError", err)
	}
	if expectedThrow != "UbagApiError" {
		t.Fatalf("unsupported expected throw %q", expectedThrow)
	}
	if want, ok := expect["error.code"].(string); ok && apiError.Code() != want {
		t.Fatalf("error.code = %q, want %q", apiError.Code(), want)
	}
	if want, ok := expect["error.retryable"].(bool); ok && apiError.Retryable() != want {
		t.Fatalf("error.retryable = %v, want %v", apiError.Retryable(), want)
	}
	if want, ok := expect["error.retry_after_ms"].(float64); ok {
		got, exists := apiError.RetryAfterMS()
		if !exists || got != int64(want) {
			t.Fatalf("error.retry_after_ms = %d/%v, want %.0f", got, exists, want)
		}
	}
}

func valueAtPath(value any, path string) (any, bool) {
	current := value
	for _, part := range strings.Split(path, ".") {
		if part == "length" {
			items, ok := current.([]any)
			if !ok {
				return nil, false
			}
			current = len(items)
			continue
		}
		if index, err := strconv.Atoi(part); err == nil {
			items, ok := current.([]any)
			if !ok || index < 0 || index >= len(items) {
				return nil, false
			}
			current = items[index]
			continue
		}
		object, ok := current.(map[string]any)
		if !ok {
			if typed, isJSON := current.(JSON); isJSON {
				object = map[string]any(typed)
				ok = true
			}
		}
		if !ok {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func fixtureEqual(got, want any) bool {
	if reflect.DeepEqual(got, want) {
		return true
	}
	gotNumber, gotOK := asFloat64(got)
	wantNumber, wantOK := asFloat64(want)
	return gotOK && wantOK && gotNumber == wantNumber
}

func asFloat64(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

func resolveSDKPlaceholders(value any) JSON {
	resolved, ok := resolvePlaceholders(value).(map[string]any)
	if !ok {
		return JSON{}
	}
	return JSON(resolved)
}

func resolvePlaceholders(value any) any {
	switch typed := value.(type) {
	case JSON:
		resolved := map[string]any{}
		for key, item := range typed {
			resolved[key] = resolvePlaceholders(item)
		}
		return resolved
	case map[string]any:
		resolved := map[string]any{}
		for key, item := range typed {
			resolved[key] = resolvePlaceholders(item)
		}
		return resolved
	case []any:
		resolved := make([]any, 0, len(typed))
		for _, item := range typed {
			resolved = append(resolved, resolvePlaceholders(item))
		}
		return resolved
	case string:
		if typed == "__SDK_NAME__" {
			return SDKName
		}
		if typed == "__SDK_VERSION__" {
			return SDKVersion
		}
		return typed
	default:
		return typed
	}
}
