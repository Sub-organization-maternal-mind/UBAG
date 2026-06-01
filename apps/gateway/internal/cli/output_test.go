package cli_test

import (
	"strings"
	"testing"

	"github.com/ubag/ubag/apps/gateway/internal/cli"
)

// ─────────────────────────────────────────────────────────────────────────────
// FormatTable
// ─────────────────────────────────────────────────────────────────────────────

func TestFormatTable_BasicStructure(t *testing.T) {
	headers := []string{"ID", "STATUS"}
	rows := [][]string{
		{"job-1", "running"},
		{"job-2", "done"},
	}
	out := cli.FormatTable(headers, rows)

	// Must contain pipe characters.
	if !strings.Contains(out, "|") {
		t.Errorf("FormatTable output has no pipes: %q", out)
	}
	// Must contain header values.
	if !strings.Contains(out, "ID") {
		t.Errorf("FormatTable output missing header ID: %q", out)
	}
	if !strings.Contains(out, "STATUS") {
		t.Errorf("FormatTable output missing header STATUS: %q", out)
	}
	// Must contain data values.
	if !strings.Contains(out, "job-1") {
		t.Errorf("FormatTable output missing job-1: %q", out)
	}
	if !strings.Contains(out, "running") {
		t.Errorf("FormatTable output missing running: %q", out)
	}
}

func TestFormatTable_SeparatorRow(t *testing.T) {
	headers := []string{"A", "B"}
	rows := [][]string{{"1", "2"}}
	lines := strings.Split(cli.FormatTable(headers, rows), "\n")

	// line 0: header row, line 1: separator, line 2: data row
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 lines, got %d: %v", len(lines), lines)
	}
	sep := lines[1]
	if !strings.Contains(sep, "-") {
		t.Errorf("separator line has no dashes: %q", sep)
	}
	if !strings.Contains(sep, "|") {
		t.Errorf("separator line has no pipes: %q", sep)
	}
}

func TestFormatTable_EmptyHeaders(t *testing.T) {
	out := cli.FormatTable(nil, nil)
	if out != "" {
		t.Errorf("expected empty output for nil headers, got %q", out)
	}
}

func TestFormatTable_ColumnAlignment(t *testing.T) {
	headers := []string{"NAME", "KIND"}
	rows := [][]string{
		{"browser", "headless"},
		{"api", "rest"},
	}
	out := cli.FormatTable(headers, rows)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	// All data lines (and header) should have the same length, guaranteeing
	// consistent column widths.
	wantLen := len(lines[0])
	for i, line := range lines {
		if line == "" {
			continue
		}
		if len(line) != wantLen {
			t.Errorf("line %d has length %d, want %d:\n  %q", i, len(line), wantLen, line)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// FormatJSON
// ─────────────────────────────────────────────────────────────────────────────

func TestFormatJSON_Indented(t *testing.T) {
	v := map[string]string{"status": "ok", "id": "j1"}
	out, err := cli.FormatJSON(v)
	if err != nil {
		t.Fatalf("FormatJSON() error: %v", err)
	}
	if !strings.Contains(out, "\n") {
		t.Errorf("FormatJSON output is not indented: %q", out)
	}
	if !strings.Contains(out, `"status"`) {
		t.Errorf("FormatJSON output missing status field: %q", out)
	}
}

func TestFormatJSON_Nil(t *testing.T) {
	out, err := cli.FormatJSON(nil)
	if err != nil {
		t.Fatalf("FormatJSON(nil) error: %v", err)
	}
	if out != "null" {
		t.Errorf("FormatJSON(nil) = %q, want null", out)
	}
}

func TestFormatJSON_Slice(t *testing.T) {
	v := []string{"a", "b", "c"}
	out, err := cli.FormatJSON(v)
	if err != nil {
		t.Fatalf("FormatJSON() error: %v", err)
	}
	if !strings.Contains(out, `"a"`) {
		t.Errorf("FormatJSON output missing a: %q", out)
	}
}
