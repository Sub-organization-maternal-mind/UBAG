package ubag

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultAPIVersion = "2026-05-22"
	SDKName           = "ubag-go"
	SDKVersion        = "0.0.0"
	jsonContentType   = "application/json"
)

const crockfordBase32 = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

type JSON map[string]any

type Client struct {
	baseURL        *url.URL
	apiVersion     string
	appSecret      string
	httpClient     *http.Client
	defaultHeaders http.Header
}

type Option func(*Client)

func WithAppSecret(secret string) Option {
	return func(client *Client) {
		client.appSecret = secret
	}
}

func WithAPIVersion(version string) Option {
	return func(client *Client) {
		client.apiVersion = version
	}
}

func WithHTTPClient(httpClient *http.Client) Option {
	return func(client *Client) {
		if httpClient != nil {
			client.httpClient = httpClient
		}
	}
}

func WithDefaultHeader(key, value string) Option {
	return func(client *Client) {
		client.defaultHeaders.Set(key, value)
	}
}

func NewClient(baseURL string, options ...Option) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("baseURL is required")
	}

	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("baseURL must include scheme and host")
	}
	if !strings.HasSuffix(parsed.Path, "/") {
		parsed.Path += "/"
	}

	client := &Client{
		baseURL:        parsed,
		apiVersion:     DefaultAPIVersion,
		httpClient:     http.DefaultClient,
		defaultHeaders: make(http.Header),
	}
	for _, option := range options {
		option(client)
	}
	if client.apiVersion == "" {
		client.apiVersion = DefaultAPIVersion
	}

	return client, nil
}

type requestConfig struct {
	apiVersion     string
	idempotencyKey string
	headers        http.Header
}

type RequestOption func(*requestConfig)

func WithRequestAPIVersion(version string) RequestOption {
	return func(config *requestConfig) {
		config.apiVersion = version
	}
}

func WithIdempotencyKey(key string) RequestOption {
	return func(config *requestConfig) {
		config.idempotencyKey = key
	}
}

func WithHeader(key, value string) RequestOption {
	return func(config *requestConfig) {
		config.headers.Set(key, value)
	}
}

type ListJobsParams struct {
	Cursor  string
	Limit   int
	Status  string
	Target  string
	Sort    string
	Fields  []string
	Include []string
}

type ListParams struct {
	Cursor string
	Limit  int
}

type ListAlertsParams struct {
	Limit  int
	Status string
}

type ListBrowserInstancesParams struct {
	Limit int
	State string
}

type ListProviderContextsParams struct {
	Limit      int
	InstanceID string
}

type ListBrowserTabsParams struct {
	Limit     int
	ContextID string
	State     string
}

type ListJobEventsParams struct {
	Cursor        string
	AfterSequence int
	Limit         int
}

type ArtifactDownload struct {
	Body        []byte
	ContentType string
	Checksum    string
}

func (client *Client) Health(ctx context.Context, options ...RequestOption) (JSON, error) {
	return client.request(ctx, http.MethodGet, "/v1/health", nil, client.resolveOptions(options...))
}

func (client *Client) Ready(ctx context.Context, options ...RequestOption) (JSON, error) {
	return client.request(ctx, http.MethodGet, "/v1/ready", nil, client.resolveOptions(options...))
}

func (client *Client) Version(ctx context.Context, options ...RequestOption) (JSON, error) {
	config := client.resolveOptions(options...)
	config.idempotencyKey = ""
	return client.request(ctx, http.MethodGet, "/v1/version", nil, config)
}

func (client *Client) CreateJob(ctx context.Context, request JSON, options ...RequestOption) (JSON, error) {
	config := client.resolveOptions(options...)
	body, err := cloneJSON(request)
	if err != nil {
		return nil, err
	}

	apiVersion := stringValue(body["api_version"])
	if apiVersion == "" {
		apiVersion = config.apiVersion
	}
	idempotencyKey := stringValue(body["idempotency_key"])
	if idempotencyKey == "" {
		idempotencyKey = config.idempotencyKey
	}
	if idempotencyKey == "" {
		idempotencyKey = GenerateIdempotencyKey(time.Now())
	}

	body["api_version"] = apiVersion
	body["idempotency_key"] = idempotencyKey
	ensureSDKMetadata(body)

	config.apiVersion = apiVersion
	config.idempotencyKey = idempotencyKey
	return client.request(ctx, http.MethodPost, "/v1/jobs", body, config)
}

func (client *Client) GetJob(ctx context.Context, jobID string, options ...RequestOption) (JSON, error) {
	return client.request(ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(jobID), nil, client.resolveOptions(options...))
}

func (client *Client) ListJobs(ctx context.Context, params ListJobsParams, options ...RequestOption) (JSON, error) {
	path := "/v1/jobs" + buildListJobsQuery(params)
	return client.request(ctx, http.MethodGet, path, nil, client.resolveOptions(options...))
}

func (client *Client) ListWorkflows(ctx context.Context, options ...RequestOption) (JSON, error) {
	return client.request(ctx, http.MethodGet, "/v1/workflows", nil, client.resolveOptions(options...))
}

func (client *Client) ListTemplates(ctx context.Context, options ...RequestOption) (JSON, error) {
	return client.request(ctx, http.MethodGet, "/v1/templates", nil, client.resolveOptions(options...))
}

func (client *Client) ListTargets(ctx context.Context, params ListParams, options ...RequestOption) (JSON, error) {
	return client.request(ctx, http.MethodGet, "/v1/targets"+buildListQuery(params), nil, client.resolveOptions(options...))
}

func (client *Client) ListAdapters(ctx context.Context, params ListParams, options ...RequestOption) (JSON, error) {
	return client.request(ctx, http.MethodGet, "/v1/adapters"+buildListQuery(params), nil, client.resolveOptions(options...))
}

func (client *Client) ListApps(ctx context.Context, params ListParams, options ...RequestOption) (JSON, error) {
	return client.request(ctx, http.MethodGet, "/v1/apps"+buildListQuery(params), nil, client.resolveOptions(options...))
}

func (client *Client) ListDevices(ctx context.Context, params ListParams, options ...RequestOption) (JSON, error) {
	return client.request(ctx, http.MethodGet, "/v1/devices"+buildListQuery(params), nil, client.resolveOptions(options...))
}

func (client *Client) ListAuditEvents(ctx context.Context, params ListParams, options ...RequestOption) (JSON, error) {
	return client.request(ctx, http.MethodGet, "/v1/audit"+buildListQuery(params), nil, client.resolveOptions(options...))
}

func (client *Client) ListWebhooks(ctx context.Context, params ListParams, options ...RequestOption) (JSON, error) {
	return client.request(ctx, http.MethodGet, "/v1/webhooks"+buildListQuery(params), nil, client.resolveOptions(options...))
}

func (client *Client) ListEvents(ctx context.Context, params ListParams, options ...RequestOption) (JSON, error) {
	return client.request(ctx, http.MethodGet, "/v1/events"+buildListQuery(params), nil, client.resolveOptions(options...))
}

func (client *Client) ListJobEvents(ctx context.Context, jobID string, params ListJobEventsParams, options ...RequestOption) (JSON, error) {
	pairs := make([][2]string, 0, 3)
	addQueryPair := func(key, value string) {
		if value != "" {
			pairs = append(pairs, [2]string{key, value})
		}
	}
	addQueryPair("cursor", params.Cursor)
	if params.AfterSequence > 0 {
		addQueryPair("after_sequence", strconv.Itoa(params.AfterSequence))
	}
	if params.Limit > 0 {
		addQueryPair("limit", strconv.Itoa(params.Limit))
	}
	return client.request(ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(jobID)+"/events"+encodeQueryPairs(pairs), nil, client.resolveOptions(options...))
}

func (client *Client) ListJobArtifacts(ctx context.Context, jobID string, options ...RequestOption) (JSON, error) {
	return client.request(ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(jobID)+"/artifacts", nil, client.resolveOptions(options...))
}

func (client *Client) PutJobArtifact(ctx context.Context, jobID, key string, body []byte, contentType string, options ...RequestOption) (JSON, error) {
	config := client.resolveOptions(options...)
	if config.idempotencyKey == "" {
		config.idempotencyKey = GenerateIdempotencyKey(time.Now())
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	responseBody, _, err := client.requestBytes(ctx, http.MethodPut, "/v1/jobs/"+url.PathEscape(jobID)+"/artifacts/"+url.PathEscape(key), body, contentType, config)
	if err != nil {
		return nil, err
	}
	var payload JSON
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (client *Client) GetJobArtifact(ctx context.Context, jobID, key string, options ...RequestOption) (*ArtifactDownload, error) {
	responseBody, headers, err := client.requestBytes(ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(jobID)+"/artifacts/"+url.PathEscape(key), nil, "", client.resolveOptions(options...))
	if err != nil {
		return nil, err
	}
	return &ArtifactDownload{
		Body:        responseBody,
		ContentType: headers.Get("Content-Type"),
		Checksum:    headers.Get("Ubag-Artifact-Checksum"),
	}, nil
}

func (client *Client) ReplayWebhookDelivery(ctx context.Context, request JSON, options ...RequestOption) (JSON, error) {
	return client.mutateGeneric(ctx, "/v1/webhooks/replay", request, options...)
}

func (client *Client) CacheStatus(ctx context.Context, options ...RequestOption) (JSON, error) {
	return client.request(ctx, http.MethodGet, "/v1/cache", nil, client.resolveOptions(options...))
}

func (client *Client) ListAlerts(ctx context.Context, params ListAlertsParams, options ...RequestOption) (JSON, error) {
	pairs := make([][2]string, 0, 2)
	if params.Limit > 0 {
		pairs = append(pairs, [2]string{"limit", strconv.Itoa(params.Limit)})
	}
	if params.Status != "" {
		pairs = append(pairs, [2]string{"status", params.Status})
	}
	return client.request(ctx, http.MethodGet, "/v1/alerts"+encodeQueryPairs(pairs), nil, client.resolveOptions(options...))
}

func (client *Client) GetAlertConfig(ctx context.Context, options ...RequestOption) (JSON, error) {
	return client.request(ctx, http.MethodGet, "/v1/alerts/config", nil, client.resolveOptions(options...))
}

func (client *Client) AcknowledgeAlert(ctx context.Context, alertID string, request JSON, options ...RequestOption) (JSON, error) {
	return client.mutateGeneric(ctx, "/v1/alerts/"+url.PathEscape(alertID)+"/acknowledge", request, options...)
}

func (client *Client) ResolveAlert(ctx context.Context, alertID string, request JSON, options ...RequestOption) (JSON, error) {
	return client.mutateGeneric(ctx, "/v1/alerts/"+url.PathEscape(alertID)+"/resolve", request, options...)
}

func (client *Client) ListBrowserInstances(ctx context.Context, params ListBrowserInstancesParams, options ...RequestOption) (JSON, error) {
	pairs := make([][2]string, 0, 2)
	if params.Limit > 0 {
		pairs = append(pairs, [2]string{"limit", strconv.Itoa(params.Limit)})
	}
	if params.State != "" {
		pairs = append(pairs, [2]string{"state", params.State})
	}
	return client.request(ctx, http.MethodGet, "/v1/browser/instances"+encodeQueryPairs(pairs), nil, client.resolveOptions(options...))
}

func (client *Client) ListProviderContexts(ctx context.Context, params ListProviderContextsParams, options ...RequestOption) (JSON, error) {
	pairs := make([][2]string, 0, 2)
	if params.Limit > 0 {
		pairs = append(pairs, [2]string{"limit", strconv.Itoa(params.Limit)})
	}
	if params.InstanceID != "" {
		pairs = append(pairs, [2]string{"instance_id", params.InstanceID})
	}
	return client.request(ctx, http.MethodGet, "/v1/browser/contexts"+encodeQueryPairs(pairs), nil, client.resolveOptions(options...))
}

func (client *Client) ListBrowserTabs(ctx context.Context, params ListBrowserTabsParams, options ...RequestOption) (JSON, error) {
	pairs := make([][2]string, 0, 3)
	if params.Limit > 0 {
		pairs = append(pairs, [2]string{"limit", strconv.Itoa(params.Limit)})
	}
	if params.ContextID != "" {
		pairs = append(pairs, [2]string{"context_id", params.ContextID})
	}
	if params.State != "" {
		pairs = append(pairs, [2]string{"state", params.State})
	}
	return client.request(ctx, http.MethodGet, "/v1/browser/tabs"+encodeQueryPairs(pairs), nil, client.resolveOptions(options...))
}

func (client *Client) GetBrowserTopologySummary(ctx context.Context, options ...RequestOption) (JSON, error) {
	return client.request(ctx, http.MethodGet, "/v1/browser/summary", nil, client.resolveOptions(options...))
}

func (client *Client) GetConcurrency(ctx context.Context, params ListParams, options ...RequestOption) (JSON, error) {
	pairs := make([][2]string, 0, 2)
	if params.Cursor != "" {
		pairs = append(pairs, [2]string{"cursor", params.Cursor})
	}
	if params.Limit > 0 {
		pairs = append(pairs, [2]string{"limit", strconv.Itoa(params.Limit)})
	}
	return client.request(ctx, http.MethodGet, "/v1/concurrency"+encodeQueryPairs(pairs), nil, client.resolveOptions(options...))
}

func (client *Client) SSOLogout(ctx context.Context, request JSON, options ...RequestOption) (JSON, error) {
	return client.mutateGeneric(ctx, "/v1/sso/logout", request, options...)
}

func (client *Client) ExportAudit(ctx context.Context, request JSON, options ...RequestOption) (JSON, error) {
	return client.mutateGeneric(ctx, "/v1/audit/export", request, options...)
}

func (client *Client) GetMetrics(ctx context.Context, options ...RequestOption) (string, error) {
	config := client.resolveOptions(options...)
	config.headers.Set("Accept", "text/plain")
	body, _, err := client.requestBytes(ctx, http.MethodGet, "/v1/metrics", nil, "", config)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func (client *Client) StreamJobEventsSSE(ctx context.Context, jobID string, options ...RequestOption) ([]byte, error) {
	config := client.resolveOptions(options...)
	config.headers.Set("Accept", "text/event-stream")
	body, _, err := client.requestBytes(ctx, http.MethodGet, "/v1/sse/jobs/"+url.PathEscape(jobID), nil, "", config)
	return body, err
}

func (client *Client) StreamEventsWebSocket(ctx context.Context, options ...RequestOption) (JSON, error) {
	config := client.resolveOptions(options...)
	config.headers.Set("Upgrade", "websocket")
	return client.request(ctx, http.MethodGet, "/v1/stream", nil, config)
}

func (client *Client) CancelJob(ctx context.Context, jobID string, request JSON, options ...RequestOption) (JSON, error) {
	return client.mutateJob(ctx, jobID, "cancel", request, options...)
}

func (client *Client) RetryJob(ctx context.Context, jobID string, request JSON, options ...RequestOption) (JSON, error) {
	return client.mutateJob(ctx, jobID, "retry", request, options...)
}

func (client *Client) DeleteJobArtifact(ctx context.Context, jobID, key string, options ...RequestOption) error {
	config := client.resolveOptions(options...)
	if config.idempotencyKey == "" {
		config.idempotencyKey = GenerateIdempotencyKey(time.Now())
	}
	_, err := client.request(ctx, http.MethodDelete, "/v1/jobs/"+url.PathEscape(jobID)+"/artifacts/"+url.PathEscape(key), nil, config)
	return err
}

func (client *Client) mutateJob(ctx context.Context, jobID, operation string, request JSON, options ...RequestOption) (JSON, error) {
	config := client.resolveOptions(options...)
	body, err := cloneJSON(request)
	if err != nil {
		return nil, err
	}

	apiVersion := stringValue(body["api_version"])
	if apiVersion == "" {
		apiVersion = config.apiVersion
	}
	idempotencyKey := stringValue(body["idempotency_key"])
	if idempotencyKey == "" {
		idempotencyKey = config.idempotencyKey
	}
	if idempotencyKey == "" {
		idempotencyKey = GenerateIdempotencyKey(time.Now())
	}

	body["api_version"] = apiVersion
	body["idempotency_key"] = idempotencyKey
	config.apiVersion = apiVersion
	config.idempotencyKey = idempotencyKey

	path := "/v1/jobs/" + url.PathEscape(jobID) + "/" + operation
	return client.request(ctx, http.MethodPost, path, body, config)
}

func (client *Client) mutateGeneric(ctx context.Context, path string, request JSON, options ...RequestOption) (JSON, error) {
	config := client.resolveOptions(options...)
	body, err := cloneJSON(request)
	if err != nil {
		return nil, err
	}

	apiVersion := stringValue(body["api_version"])
	if apiVersion == "" {
		apiVersion = config.apiVersion
	}
	idempotencyKey := stringValue(body["idempotency_key"])
	if idempotencyKey == "" {
		idempotencyKey = config.idempotencyKey
	}
	if idempotencyKey == "" {
		idempotencyKey = GenerateIdempotencyKey(time.Now())
	}

	body["api_version"] = apiVersion
	body["idempotency_key"] = idempotencyKey
	config.apiVersion = apiVersion
	config.idempotencyKey = idempotencyKey

	return client.request(ctx, http.MethodPost, path, body, config)
}

func (client *Client) request(ctx context.Context, method, path string, body JSON, config requestConfig) (JSON, error) {
	target, err := client.resolveURL(path)
	if err != nil {
		return nil, err
	}

	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(bodyBytes)
	}

	if ctx == nil {
		ctx = context.Background()
	}
	request, err := http.NewRequestWithContext(ctx, method, target, bodyReader)
	if err != nil {
		return nil, &TransportError{URL: target, Method: method, Cause: err}
	}

	request.Header.Set("Accept", jsonContentType)
	request.Header.Set("Ubag-Api-Version", config.apiVersion)
	request.Header.Set("Ubag-Sdk-Name", SDKName)
	request.Header.Set("Ubag-Sdk-Version", SDKVersion)
	for key, values := range client.defaultHeaders {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	for key, values := range config.headers {
		request.Header.Del(key)
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	if client.appSecret != "" && request.Header.Get("Authorization") == "" {
		request.Header.Set("Authorization", "Bearer "+client.appSecret)
	}
	if config.idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", config.idempotencyKey)
	}
	if body != nil {
		request.Header.Set("Content-Type", jsonContentType)
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, &TransportError{URL: target, Method: method, Cause: err}
	}
	defer response.Body.Close()

	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return nil, readErr
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, newAPIError(response, responseBody, target, method)
	}
	if len(responseBody) == 0 || response.StatusCode == http.StatusNoContent {
		return nil, nil
	}

	var payload JSON
	if err := json.Unmarshal(responseBody, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (client *Client) requestBytes(ctx context.Context, method, path string, body []byte, contentType string, config requestConfig) ([]byte, http.Header, error) {
	target, err := client.resolveURL(path)
	if err != nil {
		return nil, nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, bodyReader)
	if err != nil {
		return nil, nil, &TransportError{URL: target, Method: method, Cause: err}
	}

	request.Header.Set("Accept", "*/*")
	request.Header.Set("Ubag-Api-Version", config.apiVersion)
	request.Header.Set("Ubag-Sdk-Name", SDKName)
	request.Header.Set("Ubag-Sdk-Version", SDKVersion)
	for key, values := range client.defaultHeaders {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	for key, values := range config.headers {
		request.Header.Del(key)
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	if client.appSecret != "" && request.Header.Get("Authorization") == "" {
		request.Header.Set("Authorization", "Bearer "+client.appSecret)
	}
	if config.idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", config.idempotencyKey)
	}
	if body != nil {
		request.Header.Set("Content-Type", contentType)
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		return nil, nil, &TransportError{URL: target, Method: method, Cause: err}
	}
	defer response.Body.Close()

	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return nil, nil, readErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil, newAPIError(response, responseBody, target, method)
	}
	return responseBody, response.Header.Clone(), nil
}

func (client *Client) resolveOptions(options ...RequestOption) requestConfig {
	config := requestConfig{
		apiVersion: client.apiVersion,
		headers:    make(http.Header),
	}
	for _, option := range options {
		option(&config)
	}
	if config.apiVersion == "" {
		config.apiVersion = client.apiVersion
	}
	return config
}

func (client *Client) resolveURL(path string) (string, error) {
	relative, err := url.Parse(path)
	if err != nil {
		return "", err
	}
	return client.baseURL.ResolveReference(relative).String(), nil
}

func buildListJobsQuery(params ListJobsParams) string {
	pairs := make([][2]string, 0, 7)
	addQueryPair := func(key, value string) {
		if value != "" {
			pairs = append(pairs, [2]string{key, value})
		}
	}

	addQueryPair("cursor", params.Cursor)
	if params.Limit > 0 {
		addQueryPair("limit", strconv.Itoa(params.Limit))
	}
	addQueryPair("filter[status]", params.Status)
	addQueryPair("filter[target]", params.Target)
	addQueryPair("sort", params.Sort)
	if len(params.Fields) > 0 {
		addQueryPair("fields", strings.Join(params.Fields, ","))
	}
	if len(params.Include) > 0 {
		addQueryPair("include", strings.Join(params.Include, ","))
	}

	return encodeQueryPairs(pairs)
}

func buildListQuery(params ListParams) string {
	pairs := make([][2]string, 0, 2)
	if params.Cursor != "" {
		pairs = append(pairs, [2]string{"cursor", params.Cursor})
	}
	if params.Limit > 0 {
		pairs = append(pairs, [2]string{"limit", strconv.Itoa(params.Limit)})
	}
	return encodeQueryPairs(pairs)
}

func encodeQueryPairs(pairs [][2]string) string {
	if len(pairs) == 0 {
		return ""
	}
	encoded := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		encoded = append(encoded, url.QueryEscape(pair[0])+"="+url.QueryEscape(pair[1]))
	}
	return "?" + strings.Join(encoded, "&")
}

func GenerateIdempotencyKey(now time.Time) string {
	if now.IsZero() {
		now = time.Now()
	}
	return encodeBase32(now.UnixMilli(), 10) + randomBase32(10)
}

func ensureSDKMetadata(body JSON) {
	clientMetadata, ok := asMap(body["client"])
	if !ok {
		clientMetadata = JSON{}
		body["client"] = clientMetadata
	}
	if _, exists := clientMetadata["sdk"]; !exists {
		clientMetadata["sdk"] = JSON{
			"name":    SDKName,
			"version": SDKVersion,
		}
	}
}

func cloneJSON(input JSON) (JSON, error) {
	if input == nil {
		return JSON{}, nil
	}
	data, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	var output JSON
	if err := json.Unmarshal(data, &output); err != nil {
		return nil, err
	}
	return output, nil
}

func asMap(value any) (JSON, bool) {
	switch typed := value.(type) {
	case JSON:
		return typed, true
	case map[string]any:
		return JSON(typed), true
	default:
		return nil, false
	}
}

func stringValue(value any) string {
	typed, ok := value.(string)
	if !ok {
		return ""
	}
	return typed
}

func encodeBase32(value int64, length int) string {
	if value < 0 {
		value = 0
	}
	output := ""
	remaining := value
	for i := 0; i < length; i++ {
		output = string(crockfordBase32[remaining%32]) + output
		remaining /= 32
	}
	return output
}

func randomBase32(byteLength int) string {
	bytes := make([]byte, byteLength)
	if _, err := rand.Read(bytes); err != nil {
		for i := range bytes {
			n, _ := rand.Int(rand.Reader, big.NewInt(256))
			bytes[i] = byte(n.Int64())
		}
	}

	output := ""
	buffer := 0
	bits := 0
	for _, item := range bytes {
		buffer = (buffer << 8) | int(item)
		bits += 8
		for bits >= 5 {
			output += string(crockfordBase32[(buffer>>(bits-5))&31])
			bits -= 5
		}
	}
	return output
}
