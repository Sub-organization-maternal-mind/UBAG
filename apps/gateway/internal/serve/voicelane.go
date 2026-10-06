package serve

import (
	"log/slog"
	"strings"

	"github.com/ubag/ubag/apps/gateway/internal/executor"
	"github.com/ubag/ubag/apps/gateway/internal/topology"
	"github.com/ubag/ubag/apps/gateway/internal/voice"
)

// wireVoiceLaneExclusion turns on the exclusion between live voice sessions and
// the jobs that drive the same browser (P5.5; topology/lane.go, executor/voicelane.go).
// It returns the probe the job consumer consults (nil = off).
//
// UBAG_VOICE_LANE_EXCLUSION is an opt-in flag, inert by default (BINDING rule 5):
// it makes a job wait while a voice session holds its browser, which changes live
// job scheduling, so the owner turns it on ("1"/"true"/"yes") together with live
// voice media. A held job is deferred again on every retry with no upper bound
// (ADR-0012), which is another reason it is not on by default.
//
// The browser-lane registrations stay process-local counts unless the voice store
// is shared (postgres): only then can another replica's voice session or job
// matter, and only then are the registrations moved into the shared admission
// store. A shared voice store with no admission backend (UBAG_ADMISSION_SHARED=0)
// keeps them local and says so.
func wireVoiceLaneExclusion(voiceStore voice.Store, topo topology.Store, registry *topology.ConcurrencyRegistry, admission *topology.SQLTokenBackend) executor.VoiceLaneProbe {
	if voiceStore == nil || !envBool("UBAG_VOICE_LANE_EXCLUSION") {
		return nil
	}
	if voiceStoreIsShared() {
		if admission != nil {
			registry.UseLaneBackend(admission)
		} else {
			slog.Warn("UBAG_VOICE_STORE is shared but the shared admission store is off (UBAG_ADMISSION_SHARED=0): voice/job browser-lane registrations stay per-process, so a job on another replica is not seen by a voice admission")
		}
	}
	return &voice.LaneProbe{Store: voiceStore, Topology: topo}
}

// voiceStoreIsShared reports whether voice sessions live in a database every
// replica reads (postgres), as opposed to this process's memory or this host's
// SQLite file.
func voiceStoreIsShared() bool {
	switch strings.ToLower(strings.TrimSpace(getenv("UBAG_VOICE_STORE", "memory"))) {
	case "postgres", "postgresql":
		return true
	}
	return false
}
