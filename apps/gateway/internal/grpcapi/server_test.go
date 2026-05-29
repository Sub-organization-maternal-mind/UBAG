package grpcapi

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/idempotency"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
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

	server := NewServer(Config{
		APIVersion:  testAPIVersion,
		AppSecret:   testSecret,
		TenantID:    testTenantID,
		AppID:       testAppID,
		ActorRole:   "developer",
		Jobs:        jobstore.NewMemoryStore(),
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

	return ubagv1.NewJobServiceClient(conn)
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
