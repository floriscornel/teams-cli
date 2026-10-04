package output

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

// Table renders a table to stdout: a bordered, styled table on a terminal and
// tab-separated plain text when piped, so scripts get a stable format without
// having to strip ANSI escapes.
func (p *Printer) Table(headers []string, rows [][]string) {
	if p.json {
		return
	}
	rows = normalizeRows(len(headers), rows)
	if !p.color {
		line := strings.Join(headers, "\t")
		if strings.TrimSpace(line) != "" {
			_, _ = fmt.Fprintln(p.out, line)
		}
		for _, row := range rows {
			_, _ = fmt.Fprintln(p.out, strings.Join(row, "\t"))
		}
		return
	}
	t := table.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(lipgloss.NewStyle().Faint(true)).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return lipgloss.NewStyle().Bold(true).Padding(0, 1)
			}
			return lipgloss.NewStyle().Padding(0, 1)
		})
	if len(headers) > 0 {
		t = t.Headers(headers...)
	}
	for _, row := range rows {
		t = t.Row(row...)
	}
	_, _ = fmt.Fprintln(p.out, t.Render())
}

// Definitions prints aligned label/value pairs, the shape used by
// `teams auth status`, `teams doctor` and `teams cache info`.
func (p *Printer) Definitions(pairs [][2]string) {
	if p.json {
		return
	}
	if len(pairs) == 0 {
		return
	}
	width := 0
	for _, pair := range pairs {
		if n := len(pair[0]); n > width {
			width = n
		}
	}
	for _, pair := range pairs {
		label := pair[0] + ":"
		value := pair[1]
		if value == "" {
			_, _ = fmt.Fprintf(p.out, "%s\n", p.dim(label))
			continue
		}
		lines := strings.Split(value, "\n")
		_, _ = fmt.Fprintf(p.out, "%-*s %s\n", width+2, p.bold(label), lines[0])
		for _, extra := range lines[1:] {
			_, _ = fmt.Fprintf(p.out, "%s%s\n", strings.Repeat(" ", width+3), extra)
		}
	}
}

func normalizeRows(cols int, rows [][]string) [][]string {
	out := make([][]string, 0, len(rows))
	for _, row := range rows {
		cells := make([]string, cols)
		for i := range cells {
			if i < len(row) {
				cells[i] = sanitizeCell(row[i])
			}
		}
		out = append(out, cells)
	}
	return out
}

// sanitizeCell keeps a table on one line per row: embedded newlines and tabs
// would break both the bordered and the tab-separated renderings.
func sanitizeCell(s string) string {
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	return strings.TrimSpace(s)
}
