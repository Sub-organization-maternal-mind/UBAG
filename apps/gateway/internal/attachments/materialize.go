package attachments

import (
	"path/filepath"
	"strings"
)

// MaterializedFilename is the file name a declared attachment is written under
// when it is materialized to a local temp file (the primary's executor and a
// Helper Node's runner share it): the declared filename when it is a plain base
// name, else the key, plus an extension from the content type when it has none.
func MaterializedFilename(att Attachment, contentType string) string {
	candidate := strings.TrimSpace(att.Filename)
	if candidate == "" || candidate == "." || candidate == ".." ||
		filepath.Base(candidate) != candidate || strings.ContainsAny(candidate, `/\`) {
		candidate = att.Key
	}
	if filepath.Ext(candidate) == "" {
		candidate += ExtForContentType(contentType)
	}
	return candidate
}

// ExtForContentType maps a stored artifact content type to a file extension when
// the artifact key carried none. Provider file pickers sniff the upload by
// extension/MIME, so a sensible extension matters. Unknown types get "".
func ExtForContentType(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "text/markdown":
		return ".md"
	case "text/csv":
		return ".csv"
	case "application/json":
		return ".json"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "audio/webm":
		return ".webm"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4":
		return ".m4a"
	case "audio/ogg":
		return ".ogg"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	default:
		return ""
	}
}
