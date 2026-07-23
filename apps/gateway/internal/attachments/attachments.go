// Package attachments holds metadata-only helpers for job file attachments:
// parsing the attachment manifest carried in job.input and validating attachment
// keys. It deliberately contains no artifact-byte storage — bytes live in the
// artifacts store; this package only reasons about the manifest so that
// create-time validation, the gateway dispatch gate, and the worker-runner
// materialize all agree on exactly which keys a job declares.
package attachments

import (
	"fmt"
	"mime"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ErrorShape        = "UBAG-VALIDATION-ATTACHMENTS-SHAPE-001"
	ErrorKey          = "UBAG-VALIDATION-ATTACHMENT-KEY-001"
	ErrorKind         = "UBAG-VALIDATION-ATTACHMENT-KIND-001"
	ErrorDuplicateKey = "UBAG-VALIDATION-ATTACHMENT-DUPLICATE-KEY-001"
	ErrorCount        = "UBAG-VALIDATION-ATTACHMENTS-COUNT-001"
	ErrorContentType  = "UBAG-VALIDATION-ATTACHMENT-CONTENT-TYPE-001"
	ErrorFilename     = "UBAG-VALIDATION-ATTACHMENT-FILENAME-001"

	maxDeclaredAttachments = 32
	maxKeyRunes            = 512
	maxFilenameRunes       = 256
	maxContentTypeRunes    = 128
)

// ValidationError preserves the public contract code for a malformed declared
// attachment. Callers can map it directly into their API error envelope.
type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string          { return e.Message }
func (e *ValidationError) ValidationCode() string { return e.Code }

// ErrorCode returns the public validation code carried by err, or the
// attachments shape code for an unexpected parser error.
func ErrorCode(err error) string {
	if coded, ok := err.(interface{ ValidationCode() string }); ok {
		return coded.ValidationCode()
	}
	return ErrorShape
}

func validationError(code, format string, args ...any) error {
	return &ValidationError{Code: code, Message: fmt.Sprintf(format, args...)}
}

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
	LegacyAudio bool
}

// ValidKey mirrors the gateway's artifact-key rule (a single path segment):
// non-empty, not "." or "..", and free of "/", "\\", "?", "%", and NUL. The
// worker applies the same rule, so a key can never coerce a reader outside the
// artifact namespace.
func ValidKey(key string) bool {
	key = strings.TrimSpace(key)
	return key != "" && key != "." && key != ".." &&
		!strings.ContainsAny(key, "/\\?") && !strings.Contains(key, "%") &&
		!strings.ContainsFunc(key, unicode.IsControl)
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
			return nil, validationError(ErrorShape, "attachments must be an array")
		}
		if len(list) > maxDeclaredAttachments {
			return nil, validationError(ErrorCount, "job declares %d attachments; at most %d are allowed", len(list), maxDeclaredAttachments)
		}
		for i, item := range list {
			entry, ok := item.(map[string]any)
			if !ok {
				return nil, validationError(ErrorShape, "attachments[%d] must be an object", i)
			}
			for property := range entry {
				switch property {
				case "key", "filename", "content_type", "kind":
				default:
					return nil, validationError(ErrorShape, "attachments[%d] contains unknown property %q", i, property)
				}
			}
			key, ok := requiredString(entry, "key")
			if !ok || utf8.RuneCountInString(key) > maxKeyRunes || !ValidKey(key) {
				return nil, validationError(ErrorKey, "attachments[%d].key must be a valid single path segment", i)
			}
			if _, dup := seen[key]; dup {
				return nil, validationError(ErrorDuplicateKey, "attachments[%d].key %q is duplicated", i, key)
			}
			kind, ok := requiredString(entry, "kind")
			if !ok || !ValidKind(kind) {
				return nil, validationError(ErrorKind, "attachments[%d].kind must be one of document|image|audio|video|voice", i)
			}
			contentType, ok := requiredString(entry, "content_type")
			if !ok || utf8.RuneCountInString(contentType) > maxContentTypeRunes {
				return nil, validationError(ErrorContentType, "attachments[%d].content_type is required", i)
			}
			mediaType, _, err := mime.ParseMediaType(contentType)
			if err != nil || mediaType == "" {
				return nil, validationError(ErrorContentType, "attachments[%d].content_type is not a valid MIME type", i)
			}
			filename := ""
			if rawFilename, exists := entry["filename"]; exists {
				var filenameOK bool
				filename, filenameOK = rawFilename.(string)
				filename = strings.TrimSpace(filename)
				if !filenameOK || filename == "" || utf8.RuneCountInString(filename) > maxFilenameRunes ||
					strings.ContainsFunc(filename, unicode.IsControl) {
					return nil, validationError(ErrorFilename, "attachments[%d].filename must be 1..256 characters without control characters", i)
				}
			}
			seen[key] = struct{}{}
			out = append(out, Attachment{
				Key:         key,
				Filename:    filename,
				ContentType: strings.ToLower(strings.TrimSpace(mediaType)),
				Kind:        kind,
			})
		}
	}

	// Back-compat: a lone audio_artifact_key folds in as a single audio entry,
	// unless the same key was already declared in the attachments array.
	if audio := stringField(input, "audio_artifact_key"); audio != "" {
		if !ValidKey(audio) {
			return nil, validationError(ErrorKey, "audio_artifact_key must be a valid single path segment")
		}
		if _, dup := seen[audio]; !dup {
			out = append(out, Attachment{Key: audio, Kind: "audio", LegacyAudio: true})
		}
	}

	return out, nil
}

func requiredString(m map[string]any, key string) (string, bool) {
	value, ok := m[key].(string)
	value = strings.TrimSpace(value)
	return value, ok && value != ""
}

// stringField returns the trimmed string value of m[key], or "" when absent or
// not a string.
func stringField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
