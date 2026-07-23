// Package attachments holds metadata-only helpers for job file attachments:
// parsing the attachment manifest carried in job.input and validating attachment
// keys. It deliberately contains no artifact-byte storage — bytes live in the
// artifacts store; this package only reasons about the manifest so that
// create-time validation, the gateway dispatch gate, and the worker-runner
// materialize all agree on exactly which keys a job declares.
package attachments

import (
	"fmt"
	"strings"
)

// validKinds enumerates the attachment kinds a client may declare.
var validKinds = map[string]struct{}{
	"document": {},
	"image":    {},
	"audio":    {},
	"video":    {},
	"voice":    {},
}

// Attachment is one declared file in job.input.attachments (or the folded
// audio_artifact_key alias). Only metadata — never bytes.
type Attachment struct {
	Key         string
	Filename    string
	ContentType string
	Kind        string
}

// ValidKey mirrors the gateway's artifact-key rule (a single path segment):
// non-empty, not "." or "..", and free of "/", "\\", "?", "%", and NUL. The
// worker applies the same rule, so a key can never coerce a reader outside the
// artifact namespace.
func ValidKey(key string) bool {
	key = strings.TrimSpace(key)
	return key != "" && key != "." && key != ".." &&
		!strings.ContainsAny(key, "/\\?\x00") && !strings.Contains(key, "%")
}

// ValidKind reports whether kind is one of the accepted attachment kinds.
func ValidKind(kind string) bool {
	_, ok := validKinds[strings.TrimSpace(kind)]
	return ok
}

// DeclaredAttachments extracts the ordered, de-duplicated attachment manifest
// from a job input map. It is the single source of truth used by create-time
// validation, the dispatch gate's completion check, and the worker-runner
// materialize, so those three sites can never disagree on the declared key set.
//
// It folds the back-compat audio_artifact_key alias into a synthetic
// {Kind: "audio"} entry when that key is not already declared. Text jobs (no
// attachments and no audio_artifact_key) yield an empty slice. A structurally
// malformed attachments value (not an array, an entry that is not an object, or
// an entry with a missing/blank key) returns an error so callers fail closed
// rather than silently dropping a file. Detailed per-field validation
// (key syntax, kind enum, content-type policy) is layered on top by the caller.
func DeclaredAttachments(input map[string]any) ([]Attachment, error) {
	if input == nil {
		return nil, nil
	}

	out := make([]Attachment, 0)
	seen := make(map[string]struct{})

	if raw, ok := input["attachments"]; ok && raw != nil {
		list, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("attachments must be an array")
		}
		for i, item := range list {
			entry, ok := item.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("attachments[%d] must be an object", i)
			}
			key := stringField(entry, "key")
			if key == "" {
				return nil, fmt.Errorf("attachments[%d].key is required", i)
			}
			if _, dup := seen[key]; dup {
				return nil, fmt.Errorf("attachments[%d].key %q is duplicated", i, key)
			}
			seen[key] = struct{}{}
			out = append(out, Attachment{
				Key:         key,
				Filename:    stringField(entry, "filename"),
				ContentType: stringField(entry, "content_type"),
				Kind:        stringField(entry, "kind"),
			})
		}
	}

	// Back-compat: a lone audio_artifact_key folds in as a single audio entry,
	// unless the same key was already declared in the attachments array.
	if audio := stringField(input, "audio_artifact_key"); audio != "" {
		if _, dup := seen[audio]; !dup {
			out = append(out, Attachment{Key: audio, Kind: "audio"})
		}
	}

	return out, nil
}

// stringField returns the trimmed string value of m[key], or "" when absent or
// not a string.
func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
