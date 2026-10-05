package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ubag/ubag/apps/gateway/internal/topology"
)

// GET /v1/capabilities publishes, per target, the media it accepts and the
// voice modes it can serve, so an application can discover support before it
// submits a job or opens a voice session. Everything here is derived from the
// same sources the enforcement paths use — attachment policy from the adapter
// manifests (identical to /v1/adapters), inline message-part formats from the
// facade implementation, live-voice support from each manifest's voice block,
// and available voice accounts from the browser topology store — so the
// advertisement can never promise what create-time validation or the worker
// would refuse.
//
// Advertisement is not a login guarantee: manual_login_required stays
// authoritative, and available_accounts counts topology contexts whose last
// worker-detected login state is "authenticated". It deliberately says nothing
// about provider-side account state between jobs.

type voiceCapability struct {
	Live          bool   `json:"live"`
	UtteranceJobs bool   `json:"utterance_jobs"`
	EntryControl  string `json:"live_entry_control,omitempty"`
	Verified      string `json:"verified,omitempty"`
}

var (
	voiceCapabilityMu    sync.Mutex
	voiceCapabilityCache = map[string]voiceCapability{}
)

// resolveVoiceCapability loads and caches the target adapter's manifest voice
// block. A manifest without the block advertises live:false and derives
// utterance_jobs from the attachment policy (audio/voice kinds accepted), so
// transcription-style audio jobs and live voice never disagree.
func resolveVoiceCapability(target string) voiceCapability {
	target = strings.TrimSpace(target)
	voiceCapabilityMu.Lock()
	defer voiceCapabilityMu.Unlock()
	if v, ok := voiceCapabilityCache[target]; ok {
		return v
	}
	v := loadVoiceCapabilityFromDisk(target)
	voiceCapabilityCache[target] = v
	return v
}

func loadVoiceCapabilityFromDisk(target string) voiceCapability {
	capability := voiceCapability{}
	if !isTargetKey(target) {
		return capability
	}
	dir := adaptersDir()
	if dir == "" {
		return capability
	}
	raw, err := os.ReadFile(filepath.Join(dir, target, "manifest.json"))
	if err != nil {
		return capability
	}
	// Adapter manifests are authored on Windows and can carry a UTF-8 BOM
	// that encoding/json rejects; strip it before unmarshalling.
	raw = trimManifestBOM(raw)
	var manifest struct {
		Voice *struct {
			Live          bool   `json:"live"`
			UtteranceJobs *bool  `json:"utterance_jobs"`
			EntryControl  string `json:"live_entry_control"`
			Verified      string `json:"verified"`
		} `json:"voice"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil || manifest.Voice == nil {
		capability.UtteranceJobs = attachmentKindsAllowAudio(resolveAttachmentPolicy(target))
		return capability
	}
	capability.Live = manifest.Voice.Live
	capability.EntryControl = strings.TrimSpace(manifest.Voice.EntryControl)
	capability.Verified = strings.TrimSpace(manifest.Voice.Verified)
	if manifest.Voice.UtteranceJobs != nil {
		capability.UtteranceJobs = *manifest.Voice.UtteranceJobs
	} else {
		capability.UtteranceJobs = attachmentKindsAllowAudio(resolveAttachmentPolicy(target))
	}
	return capability
}

// attachmentKindsAllowAudio reports whether the policy accepts any audio/voice
// content at all — the condition for transcription-style utterance jobs.
func attachmentKindsAllowAudio(policy attachmentPolicy) bool {
	return len(policy.accepted["audio"]) > 0 || len(policy.accepted["voice"]) > 0
}

func trimManifestBOM(raw []byte) []byte {
	if len(raw) >= 3 && raw[0] == 0xEF && raw[1] == 0xBB && raw[2] == 0xBF {
		return raw[3:]
	}
	return raw
}

// facadeInlineMedia describes the multimodal message parts the facade accepts,
// intersected with this target's attachment policy: an inline part only
// advertises when the target would accept the same bytes declared natively.
func facadeInlineMedia(policy attachmentPolicy) map[string]any {
	images := make([]string, 0, len(facadeInlineImageMIMEs))
	for mime := range facadeInlineImageMIMEs {
		if policy.accepts("image", mime) {
			images = append(images, mime)
		}
	}
	sort.Strings(images)
	audio := make([]string, 0, 2)
	for _, spec := range facadeInlineAudioFormats {
		if policy.accepts("voice", spec.ContentType) {
			audio = append(audio, spec.ContentType)
		}
	}
	sort.Strings(audio)
	return map[string]any{
		"image_url":   images,
		"input_audio": audio,
		"remote_urls": false, // remote media fetching is excluded from this release
	}
}

// handleCapabilities implements GET /v1/capabilities (job:read).
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		s.writeMethodNotAllowed(w, r, http.MethodGet)
		return
	}
	if !s.authorizeGatewayAction(w, r, "job:read") {
		return
	}
	tenantID, _ := requestScope(r)

	entries := make([]map[string]any, 0, len(targetCatalog()))
	adapterKinds := map[string]string{}
	for _, adapter := range adapterCatalog() {
		if key, _ := adapter["key"].(string); key != "" {
			kind, _ := adapter["kind"].(string)
			adapterKinds[key] = kind
		}
	}
	for _, target := range targetCatalog() {
		key, _ := target["key"].(string)
		if key == "" {
			continue
		}
		policy := resolveAttachmentPolicy(key)
		voice := resolveVoiceCapability(key)
		entry := map[string]any{
			"target":                 key,
			"display_name":           target["display_name"],
			"kind":                   adapterKinds[key],
			"safe_mode":              target["safe_mode"],
			"manual_login_required":  target["manual_login_required"],
			"attachments":            attachmentPolicyView(policy),
			"inline_message_parts":   facadeInlineMedia(policy),
			"voice":                  voiceView(voice, s.availableVoiceAccounts(r.Context(), tenantID, key)),
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		a, _ := entries[i]["target"].(string)
		b, _ := entries[j]["target"].(string)
		return a < b
	})
	s.writeJSON(w, http.StatusOK, collectionResponse{
		APIVersion: s.apiVersion,
		Kind:       "capabilities",
		Data:       entries,
		TraceID:    traceIDFromContext(r.Context()),
	})
}

// attachmentPolicyView renders the policy in the same shape /v1/adapters
// publishes, so clients read one structure everywhere.
func attachmentPolicyView(policy attachmentPolicy) map[string]any {
	if !policy.declared {
		return map[string]any{"max_files": 0, "max_file_bytes": 0, "accepted": []map[string]any{}}
	}
	kinds := make([]string, 0, len(policy.accepted))
	for kind := range policy.accepted {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	accepted := make([]map[string]any, 0, len(kinds))
	for _, kind := range kinds {
		accepted = append(accepted, map[string]any{
			"kind":          kind,
			"content_types": append([]string(nil), policy.accepted[kind]...),
		})
	}
	return map[string]any{
		"max_files":      policy.effectiveMaxFiles(),
		"max_file_bytes": policy.effectiveMaxFileBytes(),
		"accepted":       accepted,
	}
}

func voiceView(voice voiceCapability, availableAccounts int) map[string]any {
	return map[string]any{
		"live":               voice.Live,
		"utterance_jobs":     voice.UtteranceJobs,
		"live_entry_control": voice.EntryControl,
		"verified":           voice.Verified,
		"available_accounts": availableAccounts,
	}
}

// availableVoiceAccounts counts this tenant's topology contexts for the target
// whose last worker-detected login state is "authenticated". A topology store
// that is not configured (nil-safe everywhere else too) advertises zero rather
// than guessing.
func (s *Server) availableVoiceAccounts(ctx context.Context, tenantID, target string) int {
	if s.topology == nil {
		return 0
	}
	contexts, err := s.topology.ListContexts(ctx, topology.ContextFilter{TenantID: tenantID, Limit: 1000})
	if err != nil {
		return 0
	}
	count := 0
	for _, context := range contexts {
		if context.TargetID == target && context.LoginState == "authenticated" {
			count++
		}
	}
	return count
}
