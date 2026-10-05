package helperv1

import (
	"encoding/hex"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Golden bytes pin field numbers and wire types of Fence: renumbering a field
// is a wire-breaking change and must fail here as well as in `buf breaking`.
const goldenFenceHex = "0a046a6f6231" + "1205" + "6174743132" + "1a046e6f6465" + "2007" + "32026670"

func TestFenceGoldenWireBytes(t *testing.T) {
	f := &Fence{
		JobId:            "job1",
		AttemptId:        "att12",
		NodeId:           "node",
		LeaseGeneration:  7,
		InputFingerprint: "fp",
		WorkloadVersion:  "",
	}
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	got := hex.EncodeToString(b)
	if want := goldenFenceHex; got != want {
		t.Fatalf("fence wire bytes changed:\n got %s\nwant %s", got, want)
	}
	var back Fence
	if err := proto.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(f, &back) {
		t.Fatalf("roundtrip mismatch: %v vs %v", f, &back)
	}
}

func TestAttemptEventRoundtripTerminalSemantics(t *testing.T) {
	ev := &AttemptEvent{
		AttemptId:       "att12",
		Sequence:        9,
		LeaseGeneration: 3,
		Type:            AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL,
		CreatedAt:       timestamppb.New(timestamppb.Now().AsTime()),
		Outcome: &AttemptOutcome{
			Status:            AttemptStatus_ATTEMPT_STATUS_FAILED,
			Submitted:         true,
			ReconcileRequired: true,
			StreamEndReason:   "provider_closed",
		},
	}
	b, err := proto.Marshal(&RunAttemptResponse{Event: ev})
	if err != nil {
		t.Fatal(err)
	}
	var back RunAttemptResponse
	if err := proto.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !proto.Equal(ev, back.GetEvent()) {
		t.Fatalf("roundtrip mismatch")
	}
	if o := back.GetEvent().GetOutcome(); !o.GetSubmitted() || !o.GetReconcileRequired() {
		t.Fatalf("post-submit ambiguity flags lost: %v", o)
	}
}
