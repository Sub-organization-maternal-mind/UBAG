package serve

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	jobstore "github.com/ubag/ubag/apps/gateway/internal/jobs"
)

// fakeStreamingDaemon is a warm daemon that can stream: StreamWorker is the path
// UBAG_WORKER_STREAM_INGEST takes, RunWorker must stay unused then.
type fakeStreamingDaemon struct{ streamed atomic.Int32 }

func (f *fakeStreamingDaemon) RunWorker(context.Context, executor.DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
	return nil, errors.New("batch path used")
}

func (f *fakeStreamingDaemon) StreamWorker(context.Context, executor.DispatchEnvelope, executor.EventSink) error {
	f.streamed.Add(1)
	return nil
}

// The router streams only warm-daemon jobs: per-job worker targets and voice
// control jobs stay on the batch path, and so does a daemon that cannot stream.
func TestTargetWorkerRunnerStreamsOnlyWarmDaemonJobs(t *testing.T) {
	daemon := &fakeStreamingDaemon{}
	runner := &targetWorkerRunner{
		daemon: daemon,
		fallback: executor.WorkerRunFunc(func(context.Context, executor.DispatchEnvelope) ([]jobstore.WorkerEvent, error) {
			return nil, nil
		}),
	}
	job := func(target, commandType string) executor.DispatchEnvelope {
		return executor.DispatchEnvelope{Job: executor.DispatchJob{Target: target, CommandType: commandType}}
	}

	for _, target := range []string{"chatgpt_web", "deepseek_web", "gemini_web", "mistral_lechat", "duckai_web"} {
		if !runner.StreamsJob(job(target, "chat.prompt")) {
			t.Fatalf("%s runs on the warm daemon and must stream", target)
		}
	}
	for _, tc := range []struct{ target, command string }{
		{"mock", "chat.prompt"}, {"generic_chat", "chat.prompt"}, {"chatgpt_web", "voice.activate"},
	} {
		if runner.StreamsJob(job(tc.target, tc.command)) {
			t.Fatalf("%s/%s must stay on the batch path", tc.target, tc.command)
		}
	}

	if err := runner.StreamWorker(context.Background(), job("chatgpt_web", "chat.prompt"), nil); err != nil || daemon.streamed.Load() != 1 {
		t.Fatalf("StreamWorker err=%v streamed=%d, want the daemon's StreamWorker once", err, daemon.streamed.Load())
	}

	batchOnly := &targetWorkerRunner{
		daemon:   executor.WorkerRunFunc(func(context.Context, executor.DispatchEnvelope) ([]jobstore.WorkerEvent, error) { return nil, nil }),
		fallback: executor.WorkerRunFunc(func(context.Context, executor.DispatchEnvelope) ([]jobstore.WorkerEvent, error) { return nil, nil }),
	}
	if batchOnly.StreamsJob(job("chatgpt_web", "chat.prompt")) {
		t.Fatal("a daemon runner that cannot stream must keep jobs on the batch path")
	}
}
