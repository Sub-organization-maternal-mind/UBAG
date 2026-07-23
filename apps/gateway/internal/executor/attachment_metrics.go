package executor

import "sync"

var attachmentMaterializeFailures = struct {
	sync.Mutex
	counts map[string]int64
}{counts: make(map[string]int64)}

func recordAttachmentMaterializeFailure(reason string) {
	attachmentMaterializeFailures.Lock()
	defer attachmentMaterializeFailures.Unlock()
	attachmentMaterializeFailures.counts[reason]++
}

// AttachmentMaterializeFailureSnapshot returns a concurrency-safe copy for the
// gateway Prometheus handler.
func AttachmentMaterializeFailureSnapshot() map[string]int64 {
	attachmentMaterializeFailures.Lock()
	defer attachmentMaterializeFailures.Unlock()
	out := make(map[string]int64, len(attachmentMaterializeFailures.counts))
	for reason, count := range attachmentMaterializeFailures.counts {
		out[reason] = count
	}
	return out
}

func attachmentMaterializeFailureSnapshot() map[string]int64 {
	return AttachmentMaterializeFailureSnapshot()
}
