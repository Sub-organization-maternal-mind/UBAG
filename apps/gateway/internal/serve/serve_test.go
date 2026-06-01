package serve

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ubag/ubag/apps/gateway/internal/artifacts"
	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/idempotency"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

func TestNewStoresFromEnvDefaultsToMemory(t *testing.T) {
	t.Setenv("UBAG_GATEWAY_STORE", "")
	t.Setenv("UBAG_IDEMPOTENCY_TTL_HOURS", "48")

	jobs, idempotencyStore, _, _, closeStores, err := newStoresFromEnv(context.Background())
	if err != nil {
		t.Fatalf("newStoresFromEnv returned error: %v", err)
	}
	defer closeStores()

	if _, ok := jobs.(*jobstore.MemoryStore); !ok {
		t.Fatalf("jobs store type = %T, want *jobs.MemoryStore", jobs)
	}
	if _, ok := idempotencyStore.(*idempotency.MemoryStore); !ok {
		t.Fatalf("idempotency store type = %T, want *idempotency.MemoryStore", idempotencyStore)
	}
}

func TestNewStoresFromEnvRequiresPostgresDSN(t *testing.T) {
	t.Setenv("UBAG_GATEWAY_STORE", "postgres")
	t.Setenv("UBAG_POSTGRES_DSN", "")
	t.Setenv("UBAG_DATABASE_URL", "")

	_, _, _, _, _, err := newStoresFromEnv(context.Background())
	if err == nil || !strings.Contains(err.Error(), "UBAG_POSTGRES_DSN") {
		t.Fatalf("error = %v, want missing Postgres DSN error", err)
	}
}

func TestNewStoresFromEnvRejectsUnknownStore(t *testing.T) {
	t.Setenv("UBAG_GATEWAY_STORE", "nats")

	_, _, _, _, _, err := newStoresFromEnv(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unsupported UBAG_GATEWAY_STORE") {
		t.Fatalf("error = %v, want unsupported store error", err)
	}
}

func TestNewDispatcherFromEnvDefaultsToNoop(t *testing.T) {
	t.Setenv("UBAG_EXECUTOR_MODE", "")

	dispatcher, err := newDispatcherFromEnv()
	if err != nil {
		t.Fatalf("newDispatcherFromEnv returned error: %v", err)
	}
	if _, ok := dispatcher.(executor.NoopDispatcher); !ok {
		t.Fatalf("dispatcher type = %T, want executor.NoopDispatcher", dispatcher)
	}
}

func TestNewDispatcherFromEnvNATSDefaultsAndOverrides(t *testing.T) {
	t.Setenv("UBAG_EXECUTOR_MODE", "nats")

	dispatcher, err := newDispatcherFromEnv()
	if err != nil {
		t.Fatalf("newDispatcherFromEnv default nats returned error: %v", err)
	}
	natsDispatcher, ok := dispatcher.(*executor.NATSDispatcher)
	if !ok {
		t.Fatalf("dispatcher type = %T, want *executor.NATSDispatcher", dispatcher)
	}
	if got := reflectedStringField(natsDispatcher, "url"); got != "nats://127.0.0.1:4222" {
		t.Fatalf("default url = %q", got)
	}
	if got := reflectedStringField(natsDispatcher, "streamName"); got != "UBAG_JOBS" {
		t.Fatalf("default stream = %q", got)
	}
	if got := reflectedStringField(natsDispatcher, "subject"); got != "ubag.jobs" {
		t.Fatalf("default subject = %q", got)
	}

	t.Setenv("UBAG_NATS_URL", "nats://nats:4222")
	t.Setenv("UBAG_NATS_STREAM", "CUSTOM_STREAM")
	t.Setenv("UBAG_NATS_SUBJECT", "custom.jobs")
	dispatcher, err = newDispatcherFromEnv()
	if err != nil {
		t.Fatalf("newDispatcherFromEnv custom nats returned error: %v", err)
	}
	natsDispatcher = dispatcher.(*executor.NATSDispatcher)
	if got := reflectedStringField(natsDispatcher, "url"); got != "nats://nats:4222" {
		t.Fatalf("custom url = %q", got)
	}
	if got := reflectedStringField(natsDispatcher, "streamName"); got != "CUSTOM_STREAM" {
		t.Fatalf("custom stream = %q", got)
	}
	if got := reflectedStringField(natsDispatcher, "subject"); got != "custom.jobs" {
		t.Fatalf("custom subject = %q", got)
	}
}

func TestNewDispatcherFromEnvRejectsUnknownMode(t *testing.T) {
	t.Setenv("UBAG_EXECUTOR_MODE", "bogus")

	_, err := newDispatcherFromEnv()
	if err == nil || !strings.Contains(err.Error(), "unsupported UBAG_EXECUTOR_MODE") {
		t.Fatalf("error = %v, want unsupported executor mode", err)
	}
}

func TestNewWebhookURLPolicyFromEnvRequiresExplicitAllowAnyPublicHosts(t *testing.T) {
	t.Setenv("UBAG_WEBHOOK_ALLOW_INSECURE_HTTP", "true")
	t.Setenv("UBAG_WEBHOOK_ALLOW_PRIVATE_HOSTS", "true")
	t.Setenv("UBAG_WEBHOOK_ALLOW_ANY_PUBLIC_HOST", "false")
	t.Setenv("UBAG_WEBHOOK_ALLOWED_HOSTS", "example.com, callbacks.example")

	policy := newWebhookURLPolicyFromEnv()
	if !policy.AllowInsecureHTTP || !policy.AllowPrivateHosts {
		t.Fatalf("policy booleans were not parsed: %#v", policy)
	}
	if policy.AllowAnyPublicHost {
		t.Fatal("policy enabled allow-any public hosts without explicit true value")
	}
	if got := policy.AllowedHosts; !reflect.DeepEqual(got, []string{"example.com", "callbacks.example"}) {
		t.Fatalf("allowed hosts = %#v", got)
	}

	t.Setenv("UBAG_WEBHOOK_ALLOW_ANY_PUBLIC_HOST", "true")
	if !newWebhookURLPolicyFromEnv().AllowAnyPublicHost {
		t.Fatal("policy did not parse UBAG_WEBHOOK_ALLOW_ANY_PUBLIC_HOST=true")
	}
}

func TestNewWorkerConsumerFromEnvSupportsFileAndNATSQueues(t *testing.T) {
	script := filepath.Join(t.TempDir(), "worker.py")
	if err := os.WriteFile(script, []byte("print('unused')\n"), 0o600); err != nil {
		t.Fatalf("write worker script: %v", err)
	}
	t.Setenv("UBAG_WORKER_PYTHON", os.Args[0])
	t.Setenv("UBAG_WORKER_SCRIPT", script)
	jobs := jobstore.NewMemoryStore()

	t.Setenv("UBAG_EXECUTOR_MODE", "file")
	t.Setenv("UBAG_EXECUTOR_SPOOL_DIR", t.TempDir())
	dispatcher, err := newDispatcherFromEnv()
	if err != nil {
		t.Fatalf("newDispatcherFromEnv file returned error: %v", err)
	}
	consumer, err := newWorkerConsumerFromEnv(dispatcher, jobs, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("newWorkerConsumerFromEnv file returned error: %v", err)
	}
	if consumer.Queue == nil {
		t.Fatal("file worker consumer queue is nil")
	}

	t.Setenv("UBAG_EXECUTOR_MODE", "nats")
	t.Setenv("UBAG_NATS_URL", "nats://nats:4222")
	t.Setenv("UBAG_NATS_STREAM", "CUSTOM_STREAM")
	t.Setenv("UBAG_NATS_SUBJECT", "custom.jobs")
	t.Setenv("UBAG_NATS_WORKER_DURABLE", "custom_worker")
	t.Setenv("UBAG_NATS_WORKER_ACK_WAIT_MS", "45000")
	t.Setenv("UBAG_NATS_WORKER_NAK_DELAY_MS", "2500")
	t.Setenv("UBAG_NATS_WORKER_MAX_DELIVER", "9")
	dispatcher, err = newDispatcherFromEnv()
	if err != nil {
		t.Fatalf("newDispatcherFromEnv nats returned error: %v", err)
	}
	consumer, err = newWorkerConsumerFromEnv(dispatcher, jobs, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("newWorkerConsumerFromEnv nats returned error: %v", err)
	}
	natsQueue, ok := consumer.Queue.(*executor.NATSWorkerQueue)
	if !ok {
		t.Fatalf("queue type = %T, want *executor.NATSWorkerQueue", consumer.Queue)
	}
	if got := reflectedStringField(natsQueue, "durable"); got != "custom_worker" {
		t.Fatalf("worker durable = %q", got)
	}
	if got := reflectedDurationField(natsQueue, "ackWait"); got != 45*time.Second {
		t.Fatalf("ack wait = %s", got)
	}
	if got := reflectedIntField(natsQueue, "maxDeliver"); got != 9 {
		t.Fatalf("max deliver = %d", got)
	}
}

func TestNewWorkerConsumerFromEnvRejectsInvalidNATSWorkerConfig(t *testing.T) {
	script := filepath.Join(t.TempDir(), "worker.py")
	if err := os.WriteFile(script, []byte("print('unused')\n"), 0o600); err != nil {
		t.Fatalf("write worker script: %v", err)
	}
	t.Setenv("UBAG_WORKER_PYTHON", os.Args[0])
	t.Setenv("UBAG_WORKER_SCRIPT", script)
	t.Setenv("UBAG_EXECUTOR_MODE", "nats")
	t.Setenv("UBAG_NATS_WORKER_MAX_DELIVER", "0")
	dispatcher, err := newDispatcherFromEnv()
	if err != nil {
		t.Fatalf("newDispatcherFromEnv returned error: %v", err)
	}

	_, err = newWorkerConsumerFromEnv(dispatcher, jobstore.NewMemoryStore(), nil, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "UBAG_NATS_WORKER_MAX_DELIVER") {
		t.Fatalf("error = %v, want max deliver validation", err)
	}
}

func TestNewArtifactStoreFromEnvDefaultsToMemory(t *testing.T) {
	t.Setenv("UBAG_ARTIFACT_STORE", "")

	store, err := newArtifactStoreFromEnv("memory", nil)
	if err != nil {
		t.Fatalf("newArtifactStoreFromEnv returned error: %v", err)
	}
	if _, ok := store.(*artifacts.MemoryArtifactStore); !ok {
		t.Fatalf("artifact store type = %T, want *artifacts.MemoryArtifactStore", store)
	}
}

func TestNewArtifactStoreFromEnvMinIOValidationAndFallbacks(t *testing.T) {
	t.Setenv("UBAG_ARTIFACT_STORE", "minio")
	t.Setenv("UBAG_MINIO_ENDPOINT", "")
	t.Setenv("MINIO_ENDPOINT", "")
	if _, err := newArtifactStoreFromEnv("memory", nil); err == nil || !strings.Contains(err.Error(), "UBAG_MINIO_ENDPOINT") {
		t.Fatalf("error = %v, want missing endpoint error", err)
	}

	t.Setenv("UBAG_MINIO_ENDPOINT", "minio:9000")
	t.Setenv("UBAG_MINIO_ACCESS_KEY", "")
	t.Setenv("UBAG_MINIO_SECRET_KEY", "")
	t.Setenv("MINIO_ROOT_USER", "")
	t.Setenv("MINIO_ROOT_PASSWORD", "")
	if _, err := newArtifactStoreFromEnv("memory", nil); err == nil || !strings.Contains(err.Error(), "access key") {
		t.Fatalf("error = %v, want missing credentials error", err)
	}

	t.Setenv("MINIO_ROOT_USER", "ubag")
	t.Setenv("MINIO_ROOT_PASSWORD", "secret")
	t.Setenv("UBAG_MINIO_BUCKET", " custom-bucket ")
	t.Setenv("UBAG_MINIO_USE_SSL", "true")
	store, err := newArtifactStoreFromEnv("memory", nil)
	if err != nil {
		t.Fatalf("newArtifactStoreFromEnv minio returned error: %v", err)
	}
	minioStore, ok := store.(*artifacts.MinIOArtifactStore)
	if !ok {
		t.Fatalf("artifact store type = %T, want *artifacts.MinIOArtifactStore", store)
	}
	if got := reflectedStringField(minioStore, "bucket"); got != "custom-bucket" {
		t.Fatalf("bucket = %q", got)
	}
}

func TestNewArtifactStoreFromEnvRejectsUnknownStore(t *testing.T) {
	t.Setenv("UBAG_ARTIFACT_STORE", "bogus")

	_, err := newArtifactStoreFromEnv("memory", nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported UBAG_ARTIFACT_STORE") {
		t.Fatalf("error = %v, want unsupported artifact store error", err)
	}
}

func reflectedStringField(value any, field string) string {
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	return v.FieldByName(field).String()
}

func reflectedDurationField(value any, field string) time.Duration {
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	return time.Duration(v.FieldByName(field).Int())
}

func reflectedIntField(value any, field string) int {
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	return int(v.FieldByName(field).Int())
}
