package workerdaemon

import (
	"os"
	"strings"
)

// AllowedEnv is the worker env allowlist shared by the primary's local workers and
// by Helper Node workers. It lives here (not in internal/executor) so the
// ubag-helper binary can build a worker environment without linking the executor.
var AllowedEnv = map[string]struct{}{
	"PATH":                                {},
	"PATHEXT":                             {},
	"SYSTEMROOT":                          {},
	"WINDIR":                              {},
	"TEMP":                                {},
	"TMP":                                 {},
	"HOME":                                {},
	"USERPROFILE":                         {},
	"UBAG_ADAPTER_OFFLINE":                {},
	"UBAG_WORKER_OFFLINE":                 {},
	"UBAG_LOGIN_READY_GRACE_S":            {},
	"UBAG_LOGIN_READY_EXTENDED_S":         {},
	"UBAG_REASONING_RESPONSE_TIMEOUT_S":   {},
	"UBAG_INTERACTION_ATTEMPTS":           {},
	"UBAG_CDP_ATTACH_ATTEMPTS":            {},
	"UBAG_PROVIDER_CONFIG_CHATGPT_WEB":    {},
	"UBAG_PROVIDER_CONFIG_GEMINI_WEB":     {},
	"UBAG_PROVIDER_CONFIG_DEEPSEEK_WEB":   {},
	"UBAG_PROVIDER_CONFIG_MISTRAL_LECHAT": {},
	"UBAG_PROVIDER_CONFIG_DUCKAI_WEB":     {},
	"UBAG_PROVIDER_CONFIG_ENABLED":        {},
	"UBAG_NEW_CHAT_ENABLED":               {},
	"UBAG_EMPTINESS_PROBE_MS":             {},
	"UBAG_RESUME_CONFIRM_MS":              {},
	"UBAG_REASONING_SETTLE_S":             {},
	"UBAG_INDICATOR_GONE_GRACE_S":         {},
	"UBAG_WARM_RELOAD_EVERY":              {},
	"UBAG_WARM_RELOAD_HEAP_MB":            {},
	"UBAG_PROFILE_DIR":                    {},
	"UBAG_BROWSER_ENGINE":                 {},
	"UBAG_BROWSER_HEADED":                 {},
	"UBAG_BROWSER_PROTOCOL":               {},
	"UBAG_NOVNC_BASE_URL":                 {},
	"UBAG_REMOTE_BROWSER_ENDPOINT":        {},
	"UBAG_WORKER_SINGLE_USER_EDGE":        {},
	// Chat ledger: lets the worker record the chats it creates so the chat
	// reaper can only ever delete UBAG's own (never the operator's). Both are
	// non-secret Ã¢â‚¬â€ a boolean and a file path Ã¢â‚¬â€ so they respect the reason this
	// allowlist exists: keep credentials (app secret, DSNs) out of the worker
	// process, not withhold benign operational config.
	"UBAG_CHAT_LEDGER_ENABLED": {},
	"UBAG_CHAT_LEDGER_PATH":    {},
	// Slot/stream/strict knobs and the mock benchmark gate: all non-secret
	// flags. UBAG_ORCHESTRATOR_ENABLED is deliberately NOT forwarded (slot
	// mode refuses it).
	"UBAG_WORKER_SLOT_ID":                 {},
	"UBAG_WORKER_IDENTITY_LOCK":           {},
	"UBAG_WORKER_STREAM_EVENTS":           {},
	"UBAG_WORKER_STREAM_MAX_TOKEN_EVENTS": {},
	"UBAG_WORKER_STRICT_STREAM_END":       {},
	"UBAG_WORKER_STRICT_SUBMIT":           {},
	"UBAG_MOCK_SYNTHETIC":                 {},
	// Synthetic chat fixture (tools/synthetic-provider): a boolean and a loopback URL the
	// worker validates itself. UBAG_WORKER_STAGE_TIMINGS is the stage-attribution flag (P0.9b).
	"UBAG_SYNTHETIC_PROVIDER":     {},
	"UBAG_SYNTHETIC_PROVIDER_URL": {},
	"UBAG_WORKER_STAGE_TIMINGS":   {},
}

// FilterEnv returns the allowlisted environment minus the keys in deny
// (upper-case). deny nil == the local worker env, unchanged.
func FilterEnv(deny map[string]struct{}) []string {
	env := []string{}
	for _, item := range os.Environ() {
		key, _, found := strings.Cut(item, "=")
		if !found {
			continue
		}
		upper := strings.ToUpper(key)
		if _, ok := AllowedEnv[upper]; !ok {
			continue
		}
		if _, blocked := deny[upper]; blocked {
			continue
		}
		env = append(env, item)
	}
	return env
}
