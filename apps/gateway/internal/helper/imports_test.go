package helper

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/jobs"
	"github.com/ubag/ubag/apps/gateway/internal/nodes"
	helperv1 "github.com/ubag/ubag/packages/proto/gen/go/ubag/helper/v1"
)

// The ubag-helper binary must stay small and credential-free by construction:
// its service core may link gRPC and the generated proto, and nothing else.
// internal/executor was measured at 228 non-standard-library packages (wazero,
// the MinIO client, pgx, the NATS client and the alerts/plugins/topology/jobs
// stack) against 114 for the whole helper binary, which is why the runner is
// an interface here and the real one is extracted into its own package later
// (P4.12) instead of importing the executor. This test is the tripwire.
func TestServiceCoreAndBinaryImportOnlyGRPCAndTheProto(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	allowed := []string{
		"github.com/ubag/ubag/packages/proto/gen/go/",
		"google.golang.org/grpc", "google.golang.org/protobuf", "google.golang.org/genproto",
		"golang.org/x/",
	}
	for _, target := range []struct{ pkg, self string }{
		{".", "github.com/ubag/ubag/apps/gateway/internal/helper"},
		{"../../cmd/ubag-helper", "github.com/ubag/ubag/apps/gateway/cmd/ubag-helper"},
	} {
		out, err := exec.Command(goBin, "list", "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", target.pkg).Output()
		if err != nil {
			t.Fatalf("go list %s: %v", target.pkg, err)
		}
	deps:
		for _, dep := range strings.Fields(string(out)) {
			if dep == target.self || dep == "github.com/ubag/ubag/apps/gateway/internal/helper" {
				continue
			}
			for _, ok := range allowed {
				if strings.HasPrefix(dep, ok) {
					continue deps
				}
			}
			t.Errorf("%s imports %s: the Helper Node binary must not link it (database, queue, object-store, executor and primary-side packages stay out)", target.pkg, dep)
		}
	}
}

// The helper cannot import the executor, jobs or nodes (see above), so the few
// constants it must agree on with the primary are duplicated. These checks make
// drift a test failure instead of a production surprise. (Test-only imports:
// they do not reach the binary.)
func TestSharedConstantsMatchThePrimary(t *testing.T) {
	if maxEventDataBytes != executor.DefaultHelperMaxEventBytes ||
		maxAttemptBytes != executor.DefaultHelperMaxAttemptBytes ||
		maxAttemptEvents != executor.DefaultHelperMaxAttemptEvents {
		t.Errorf("output bounds drifted from the primary's ingest limits: helper %d/%d/%d, executor %d/%d/%d",
			maxEventDataBytes, maxAttemptBytes, maxAttemptEvents,
			executor.DefaultHelperMaxEventBytes, executor.DefaultHelperMaxAttemptBytes, executor.DefaultHelperMaxAttemptEvents)
	}
	if NodeURISANPrefix != nodes.URISANPrefix {
		t.Errorf("node identity namespace drifted: %q vs %q", NodeURISANPrefix, nodes.URISANPrefix)
	}
	for _, id := range []string{"att_a", "att_" + strings.Repeat("Z", 124), "att_" + strings.Repeat("Z", 125), "att_", "att_a-b", "Att_a", "att_é", "", "x"} {
		if attemptRe.MatchString(id) != jobs.ValidAttemptID(id) {
			t.Errorf("attempt id %q: helper says %v, the ledger says %v", id, attemptRe.MatchString(id), jobs.ValidAttemptID(id))
		}
	}
	for _, id := range []string{"helper-1", "a", "A.b_c-d", strings.Repeat("x", 64), strings.Repeat("x", 65), "-x", ".x", "x y", ""} {
		_, ok := nodes.NodeIDFromURISAN(nodes.URISANPrefix + id)
		if nodeIDRe.MatchString(id) != ok {
			t.Errorf("node id %q: helper says %v, the registry says %v", id, nodeIDRe.MatchString(id), ok)
		}
	}
	// emit treats TERMINAL as the highest event type; a new enum value must be
	// handled there before it can be accepted.
	var highest helperv1.AttemptEventType
	for v := range helperv1.AttemptEventType_name {
		highest = max(highest, helperv1.AttemptEventType(v))
	}
	if highest != helperv1.AttemptEventType_ATTEMPT_EVENT_TYPE_TERMINAL {
		t.Errorf("AttemptEventType gained a value above TERMINAL (%v): update attempt.emit", highest)
	}
}
