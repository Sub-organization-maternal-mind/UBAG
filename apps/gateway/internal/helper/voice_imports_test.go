package helper

import (
	"os/exec"
	"strings"
	"testing"
)

// Helper-hosted voice terminates WebRTC on the node, which needs voice.MediaHub and
// therefore pion and the voice stores. That must not leak into the default
// ubag-helper binary (117 non-standard packages, gRPC and the proto and the worker
// pool only): the media endpoint is compiled in only with the helpervoice tag, and
// even then none of the primary's executor, job, artifact, node, HTTP or
// object-store/queue code may be linked.
func TestVoiceMediaIsLinkedOnlyWithTheHelperVoiceTagAndNeverTheExecutor(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain not on PATH")
	}
	deps := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(goBin, append(append([]string{"list"}, args...), "-deps", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "../../cmd/ubag-helper")...).Output()
		if err != nil {
			t.Fatalf("go list %v: %v", args, err)
		}
		return "\n" + string(out)
	}
	hasPkg := func(list, pkg string) bool {
		return strings.Contains(list, "\n"+pkg+"\n") || strings.Contains(list, "\n"+pkg+"/")
	}

	const (
		voicePkg  = "github.com/ubag/ubag/apps/gateway/internal/voice"
		voicehub  = "github.com/ubag/ubag/apps/gateway/internal/helper/voicehub"
		gatewayPf = "github.com/ubag/ubag/apps/gateway/internal/"
	)
	def := deps()
	for _, pkg := range []string{voicePkg, voicehub, "github.com/pion/webrtc/v4", "github.com/jackc/pgx/v5", "modernc.org/sqlite"} {
		if hasPkg(def, pkg) {
			t.Errorf("the default ubag-helper binary links %s: helper-hosted voice must stay behind the helpervoice tag", pkg)
		}
	}

	tagged := deps("-tags", "helpervoice")
	for _, pkg := range []string{voicePkg, voicehub, "github.com/pion/webrtc/v4"} {
		if !hasPkg(tagged, pkg) {
			t.Errorf("the helpervoice build does not link %s", pkg)
		}
	}
	for _, pkg := range []string{
		gatewayPf + "executor", gatewayPf + "jobs", gatewayPf + "artifacts", gatewayPf + "nodes", gatewayPf + "httpapi",
		gatewayPf + "serve", "github.com/minio/minio-go", "github.com/nats-io/nats.go", "github.com/tetratelabs/wazero",
	} {
		if hasPkg(tagged, pkg) {
			t.Errorf("the helpervoice build links %s: the node must not carry primary-side code", pkg)
		}
	}
}
