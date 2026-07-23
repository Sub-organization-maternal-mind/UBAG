package attachments

import "testing"

func TestDeclaredAttachmentsTextJob(t *testing.T) {
	got, err := DeclaredAttachments(map[string]any{"prompt": "hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no attachments, got %d", len(got))
	}
}

func TestDeclaredAttachmentsManifestOrderAndFields(t *testing.T) {
	input := map[string]any{
		"attachments": []any{
			map[string]any{"key": "a.pdf", "filename": "a.pdf", "content_type": "application/pdf", "kind": "document"},
			map[string]any{"key": "b.webm", "content_type": "audio/webm", "kind": "voice"},
		},
	}
	got, err := DeclaredAttachments(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 || got[0].Key != "a.pdf" || got[1].Kind != "voice" {
		t.Fatalf("unexpected result: %+v", got)
	}
	if got[0].ContentType != "application/pdf" || got[0].Filename != "a.pdf" {
		t.Fatalf("field extraction wrong: %+v", got[0])
	}
}

func TestDeclaredAttachmentsFoldsAudioAlias(t *testing.T) {
	got, err := DeclaredAttachments(map[string]any{"audio_artifact_key": "note.webm"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Key != "note.webm" || got[0].Kind != "audio" {
		t.Fatalf("audio alias not folded: %+v", got)
	}
}

func TestDeclaredAttachmentsAudioAliasNotDoubleCounted(t *testing.T) {
	input := map[string]any{
		"attachments":        []any{map[string]any{"key": "note.webm", "content_type": "audio/webm", "kind": "voice"}},
		"audio_artifact_key": "note.webm",
	}
	got, err := DeclaredAttachments(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 (deduped), got %d: %+v", len(got), got)
	}
}

func TestDeclaredAttachmentsErrors(t *testing.T) {
	cases := []map[string]any{
		{"attachments": "not-an-array"},
		{"attachments": []any{"not-an-object"}},
		{"attachments": []any{map[string]any{"content_type": "text/plain"}}}, // missing key
		{"attachments": []any{
			map[string]any{"key": "dup"},
			map[string]any{"key": "dup"},
		}},
	}
	for i, input := range cases {
		if _, err := DeclaredAttachments(input); err == nil {
			t.Fatalf("case %d: expected error, got nil", i)
		}
	}
}

func TestValidKey(t *testing.T) {
	valid := []string{"report.pdf", "a", "note.webm", "x-1_2.txt"}
	for _, k := range valid {
		if !ValidKey(k) {
			t.Fatalf("expected %q valid", k)
		}
	}
	invalid := []string{"", ".", "..", "a/b", "a\\b", "a%20b", "a?b", "a\x00b"}
	for _, k := range invalid {
		if ValidKey(k) {
			t.Fatalf("expected %q invalid", k)
		}
	}
}

func TestValidKind(t *testing.T) {
	for _, k := range []string{"document", "image", "audio", "video", "voice"} {
		if !ValidKind(k) {
			t.Fatalf("expected %q valid kind", k)
		}
	}
	if ValidKind("archive") || ValidKind("") {
		t.Fatal("expected invalid kinds to be rejected")
	}
}
