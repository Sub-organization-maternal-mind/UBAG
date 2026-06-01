package cli

import (
	"encoding/json"
	"strings"
)

// FormatTable renders headers and rows as a simple ASCII pipe-delimited table.
//
//	| col1 | col2 |
//	|------|------|
//	| val1 | val2 |
func FormatTable(headers []string, rows [][]string) string {
	if len(headers) == 0 {
		return ""
	}

	// Compute column widths: max of header width and each cell width.
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	var b strings.Builder

	// Header row.
	writeRow(&b, headers, widths)
	// Separator row.
	writeSep(&b, widths)
	// Data rows.
	for _, row := range rows {
		// Pad row to header length.
		padded := make([]string, len(headers))
		copy(padded, row)
		writeRow(&b, padded, widths)
	}

	return b.String()
}

func writeRow(b *strings.Builder, cells []string, widths []int) {
	b.WriteByte('|')
	for i, w := range widths {
		cell := ""
		if i < len(cells) {
			cell = cells[i]
		}
		b.WriteByte(' ')
		b.WriteString(cell)
		// Pad to column width.
		for j := len(cell); j < w; j++ {
			b.WriteByte(' ')
		}
		b.WriteString(" |")
	}
	b.WriteByte('\n')
}

func writeSep(b *strings.Builder, widths []int) {
	b.WriteByte('|')
	for _, w := range widths {
		// w chars of dashes plus 2 spaces (one on each side).
		b.WriteByte('-')
		for j := 0; j < w; j++ {
			b.WriteByte('-')
		}
		b.WriteString("-|")
	}
	b.WriteByte('\n')
}

// FormatJSON returns a pretty-printed JSON representation of v.
func FormatJSON(v any) (string, error) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(data), nil
}
