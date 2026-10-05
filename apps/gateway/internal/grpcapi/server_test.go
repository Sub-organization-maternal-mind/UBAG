package grpcapi

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/ubag/ubag/apps/gateway/internal/abac"
	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/idempotency"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/mfa"
	ubagv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/v1"
)

const (
	testAPIVersion = "2026-05-22"
	testSecret     = "super-secret-app-token"
	testTenantID   = "tenant_edge"
	testAppID      = "app_default"
)

func newTestClient(t *testing.T) ubagv1.JobServiceClient {
	t.Helper()
	client, _ := newTestClientWithStore(t)
	return client
}

// newTestClientWithStore also returns the backing job store so tests can land
// worker events the way the worker consumer does.
func newTestClientWithStore(t *testing.T) (ubagv1.JobServiceClient, jobstore.Store) {
	t.Helper()

	store := jobstore.NewMemoryStore()
	server := NewServer(Config{
		APIVersion:  testAPIVersion,
		AppSecret:   testSecret,
		TenantID:    testTenantID,
		AppID:       testAppID,
		ActorRole:   "developer",
		Jobs:        store,
		Idempotency: idempotency.NewMemoryStore(time.Hour),
		Executor:    executor.NewNoopDispatcher(),
	})

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	ubagv1.RegisterJobServiceServer(grpcServer, server)
	go func() {
		_ = grpcServer.Serve(listener)
	}()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) {
			return listener.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		grpcServer.Stop()
		_ = listener.Close()
	})

	return ubagv1.NewJobServiceClient(conn), store
}

func authContext(ctx context.Context, token string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)
}

func validCreateRequest(idempotencyKey string) *ubagv1.CreateJobRequest {
	return &ubagv1.CreateJobRequest{
		ApiVersion:     testAPIVersion,
		IdempotencyKey: idempotencyKey,
		Client: &ubagv1.Client{
			AppId:      "console",
			AppVersion: "1.0.0",
			SdkName:    "ubag-sdk-go",
			SdkVersion: "0.1.0",
		},
		Job: &ubagv1.JobSpec{
			Target:      "mock",
			CommandType: "chat",
			InputJson:   `{"prompt":"hello"}`,
		},
	}
}

func TestCreateJobRejectsMissingAndInvalidAuth(t *testing.T) {
	client := newTestClient(t)
	ctx := context.Background()

	if _, err := client.CreateJob(ctx, validCreateRequest("idem-key-000000000001")); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing auth: got code %v, want Unauthenticated (err=%v)", status.Code(err), err)
	}

	badCtx := authContext(ctx, "wrong-secret")
	if _, err := client.CreateJob(badCtx, validCreateRequest("idem-key-000000000001")); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("invalid auth: got code %v, want Unauthenticated (err=%v)", status.Code(err), err)
	}
}

func TestCreateJobRequiresIdempotencyKey(t *testing.T) {
	client := newTestClient(t)
	ctx := authContext(context.Background(), testSecret)

	req := validCreateRequest("")
	if _, err := client.CreateJob(ctx, req); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing idempotency key: got code %v, want InvalidArgument (err=%v)", status.Code(err), err)
	}
}

// TestAuthorizeMFAAndABACParity mirrors the httpapi.authorizeGatewayAction
// gates onto the gRPC authorization path: after RBAC passes, an ABAC deny
// rejects the action, and the privileged actions are denied whenever MFA is
// enabled (the gRPC transport carries no MFA session marker).
func TestAuthorizeMFAAndABACParity(t *testing.T) {
	denyAll, err := abac.NewEnforcer(abac.PolicyBundle{Rules: []abac.Rule{
		{Name: "deny-all", Condition: `principal["role"] == "nobody"`},
	}})
	if err != nil {
		t.Fatalf("abac.NewEnforcer: %v", err)
	}

	newServer := func(abacEnforcer *abac.Enforcer, mfaSvc *mfa.Service) *Server {
		return NewServer(Config{
			APIVersion: testAPIVersion,
			AppSecret:  testSecret,
			TenantID:   testTenantID,
			AppID:      testAppID,
			ActorRole:  "admin",
			ABAC:       abacEnforcer,
			MFA:        mfaSvc,
		})
	}

	// authorize() reads incoming metadata directly (no wire round-trip like
	// the bufconn tests), so the credential must be incoming metadata.
	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("authorization", "Bearer "+testSecret),
	)

	// Baseline: RBAC alone (no ABAC, MFA disabled) allows a job action.
	if err := newServer(nil, nil).authorize(ctx, "job:create"); err != nil {
		t.Fatalf("baseline authorize job:create: %v", err)
	}

	// ABAC deny -> PermissionDenied even though RBAC allows.
	if err := newServer(denyAll, nil).authorize(ctx, "job:create"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("abac-denied action: got code %v, want PermissionDenied (err=%v)", status.Code(err), err)
	}

	// MFA enabled + privileged action -> PermissionDenied over gRPC.
	if err := newServer(nil, &mfa.Service{}).authorize(ctx, "role:manage"); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("mfa-gated action: got code %v, want PermissionDenied (err=%v)", status.Code(err), err)
	}

	// MFA enabled + non-privileged action is unaffected.
	if err := newServer(nil, &mfa.Service{}).authorize(ctx, "job:create"); err != nil {
		t.Fatalf("mfa-enabled non-privileged action: %v", err)
	}

	// Unauthenticated context is still rejected first.
	if err := newServer(nil, nil).authorize(context.Background(), "job:create"); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("unauthenticated authorize: got code %v, want Unauthenticated", status.Code(err))
	}
}

func TestCreateJobAndGetJob(t *testing.T) {
	client := newTestClient(t)
	ctx := authContext(context.Background(), testSecret)

	created, err := client.CreateJob(ctx, validCreateRequest("idem-key-000000000001"))
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if created.GetJobId() == "" {
		t.Fatal("create job returned empty job id")
	}
	if created.GetTarget() != "mock" {
		t.Fatalf("created target = %q, want mock", created.GetTarget())
	}
	if created.GetIdempotentReplay() {
		t.Fatal("first create should not be an idempotent replay")
	}

	fetched, err := client.GetJob(ctx, &ubagv1.GetJobRequest{JobId: created.GetJobId()})
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if fetched.GetJobId() != created.GetJobId() {
		t.Fatalf("get job id = %q, want %q", fetched.GetJobId(), created.GetJobId())
	}
}

func TestCreateJobIdempotentReplay(t *testing.T) {
	client := newTestClient(t)
	ctx := authContext(context.Background(), testSecret)

	first, err := client.CreateJob(ctx, validCreateRequest("idem-key-000000000099"))
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	second, err := client.CreateJob(ctx, validCreateRequest("idem-key-000000000099"))
	if err != nil {
		t.Fatalf("replay create: %v", err)
	}
	if second.GetJobId() != first.GetJobId() {
		t.Fatalf("replay job id = %q, want %q", second.GetJobId(), first.GetJobId())
	}
	if !second.GetIdempotentReplay() {
		t.Fatal("replay response should set idempotent_replay")
	}
}

// --- StreamJobEvents characterization (zero behaviour change) -----------------

func createStreamTestJob(t *testing.T, client ubagv1.JobServiceClient, ctx context.Context, key string) *ubagv1.JobResponse {
	t.Helper()
	created, err := client.CreateJob(ctx, validCreateRequest(key))
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	return created
}

func applyStreamTestEvent(t *testing.T, store jobstore.Store, jobID, eventID, eventType string, sequence int, data map[string]any) {
	t.Helper()
	job, found, err := store.Get(context.Background(), jobID)
	if err != nil || !found {
		t.Fatalf("Get job found=%v err=%v", found, err)
	}
	if _, found, err := store.ApplyWorkerEvent(context.Background(), jobstore.WorkerEvent{
		EventID: eventID, JobID: jobID, APIVersion: job.APIVersion, Type: eventType,
		Sequence: sequence, TraceID: job.TraceID, Data: data,
	}); err != nil || !found {
		t.Fatalf("ApplyWorkerEvent %s found=%v err=%v", eventID, found, err)
	}
}

func completedStreamData() map[string]any {
	return map[string]any{"status": "completed", "result": map[string]any{"type": "text", "text": "done"}}
}

// recvTypes drains the stream until it ends and returns the event types plus
// the terminating error (io.EOF for a clean close).
func recvTypes(t *testing.T, stream ubagv1.JobService_StreamJobEventsClient) ([]string, error) {
	t.Helper()
	var types []string
	for {
		event, err := stream.Recv()
		if err != nil {
			return types, err
		}
		types = append(types, event.GetType())
	}
}

func TestStreamJobEventsReplaysHistoryAndClosesCleanlyOnTerminalEvent(t *testing.T) {
	client, store := newTestClientWithStore(t)
	ctx, cancel := context.WithTimeout(authContext(context.Background(), testSecret), 10*time.Second)
	defer cancel()
	created := createStreamTestJob(t, client, ctx, "idem-key-stream-0001")
	applyStreamTestEvent(t, store, created.GetJobId(), "stream_evt_running", "running", 2, map[string]any{"status": "running"})
	applyStreamTestEvent(t, store, created.GetJobId(), "stream_evt_done", "completed", 3, completedStreamData())

	stream, err := client.StreamJobEvents(ctx, &ubagv1.ListJobEventsRequest{JobId: created.GetJobId()})
	if err != nil {
		t.Fatalf("StreamJobEvents: %v", err)
	}
	types, err := recvTypes(t, stream)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("stream ended with %v, want clean io.EOF after the terminal event", err)
	}
	want := []string{"queued", "running", "completed"}
	if len(types) != len(want) {
		t.Fatalf("event types = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("event types = %v, want %v", types, want)
		}
	}
}

func TestStreamJobEventsHonorsAfterSequence(t *testing.T) {
	client, store := newTestClientWithStore(t)
	ctx, cancel := context.WithTimeout(authContext(context.Background(), testSecret), 10*time.Second)
	defer cancel()
	created := createStreamTestJob(t, client, ctx, "idem-key-stream-0002")
	applyStreamTestEvent(t, store, created.GetJobId(), "stream_evt_after_done", "completed", 2, completedStreamData())

	stream, err := client.StreamJobEvents(ctx, &ubagv1.ListJobEventsRequest{JobId: created.GetJobId(), AfterSequence: 1})
	if err != nil {
		t.Fatalf("StreamJobEvents: %v", err)
	}
	types, err := recvTypes(t, stream)
	if !errors.Is(err, io.EOF) || len(types) != 1 || types[0] != "completed" {
		t.Fatalf("after_sequence=1 got types=%v err=%v, want only the completed event then io.EOF", types, err)
	}
}

func TestStreamJobEventsWaitsForLiveEventsThenClosesOnTerminal(t *testing.T) {
	client, store := newTestClientWithStore(t)
	ctx, cancel := context.WithTimeout(authContext(context.Background(), testSecret), 10*time.Second)
	defer cancel()
	created := createStreamTestJob(t, client, ctx, "idem-key-stream-0003")

	stream, err := client.StreamJobEvents(ctx, &ubagv1.ListJobEventsRequest{JobId: created.GetJobId()})
	if err != nil {
		t.Fatalf("StreamJobEvents: %v", err)
	}
	first, err := stream.Recv()
	if err != nil || first.GetType() != "queued" {
		t.Fatalf("first event = %v err=%v, want queued", first, err)
	}
	applyStreamTestEvent(t, store, created.GetJobId(), "stream_live_running", "running", 2, map[string]any{"status": "running"})
	running, err := stream.Recv()
	if err != nil || running.GetType() != "running" {
		t.Fatalf("live event = %v err=%v, want running", running, err)
	}
	applyStreamTestEvent(t, store, created.GetJobId(), "stream_live_done", "completed", 3, completedStreamData())
	done, err := stream.Recv()
	if err != nil || done.GetType() != "completed" {
		t.Fatalf("terminal event = %v err=%v, want completed", done, err)
	}
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("after terminal event stream returned %v, want io.EOF", err)
	}
}

func TestStreamJobEventsStaysOpenWithoutTerminalUntilContextEnds(t *testing.T) {
	client, store := newTestClientWithStore(t)
	base := authContext(context.Background(), testSecret)
	created := createStreamTestJob(t, client, base, "idem-key-stream-0004")
	applyStreamTestEvent(t, store, created.GetJobId(), "stream_open_running", "running", 2, map[string]any{"status": "running"})

	ctx, cancel := context.WithTimeout(base, 300*time.Millisecond)
	defer cancel()
	stream, err := client.StreamJobEvents(ctx, &ubagv1.ListJobEventsRequest{JobId: created.GetJobId()})
	if err != nil {
		t.Fatalf("StreamJobEvents: %v", err)
	}
	types, err := recvTypes(t, stream)
	if errors.Is(err, io.EOF) {
		t.Fatalf("stream closed cleanly without a terminal event (types=%v)", types)
	}
	if code := status.Code(err); code != codes.DeadlineExceeded {
		t.Fatalf("stream ended with code %v (%v), want DeadlineExceeded from the client context", code, err)
	}
	if len(types) != 2 || types[0] != "queued" || types[1] != "running" {
		t.Fatalf("event types = %v, want [queued running] before the deadline", types)
	}
}

func TestStreamJobEventsRejectsBadRequests(t *testing.T) {
	client := newTestClient(t)
	authed := authContext(context.Background(), testSecret)
	created := createStreamTestJob(t, client, authed, "idem-key-stream-0005")

	expect := func(name string, ctx context.Context, req *ubagv1.ListJobEventsRequest, want codes.Code) {
		t.Helper()
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		stream, err := client.StreamJobEvents(ctx, req)
		if err == nil {
			_, err = stream.Recv()
		}
		if status.Code(err) != want {
			t.Fatalf("%s: code = %v (%v), want %v", name, status.Code(err), err, want)
		}
	}
	expect("missing auth", context.Background(), &ubagv1.ListJobEventsRequest{JobId: created.GetJobId()}, codes.Unauthenticated)
	expect("missing job", authed, &ubagv1.ListJobEventsRequest{JobId: "job_missing_stream"}, codes.NotFound)
	expect("empty job id", authed, &ubagv1.ListJobEventsRequest{}, codes.InvalidArgument)
	expect("negative after_sequence", authed, &ubagv1.ListJobEventsRequest{JobId: created.GetJobId(), AfterSequence: -1}, codes.InvalidArgument)
}
