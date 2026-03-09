package display

import (
	"fmt"
	"io"
	"strings"
)

// Table is a simple left-aligned column table printer that requires no
// external TUI dependencies.
type Table struct {
	Headers []string
	Rows    [][]string
}

// AddRow appends a row to the table.
func (t *Table) AddRow(cols ...string) {
	t.Rows = append(t.Rows, cols)
}

// Print writes the table to w, computing column widths automatically.
func (t *Table) Print(w io.Writer) {
	widths := make([]int, len(t.Headers))
	for i, h := range t.Headers {
		widths[i] = len(h)
	}
	for _, row := range t.Rows {
		for i, col := range row {
			if i < len(widths) && len(col) > widths[i] {
				widths[i] = len(col)
			}
		}
	}

	printRow(w, t.Headers, widths)

	sep := make([]string, len(t.Headers))
	for i, w2 := range widths {
		sep[i] = strings.Repeat("-", w2)
	}
	printRow(w, sep, widths)

	for _, row := range t.Rows {
		printRow(w, row, widths)
	}
}

func printRow(w io.Writer, cols []string, widths []int) {
	for i, col := range cols {
		if i > 0 {
			fmt.Fprint(w, "  ")
		}
		if i < len(widths) {
			fmt.Fprintf(w, "%-*s", widths[i], col)
		} else {
			fmt.Fprint(w, col)
		}
	}
	fmt.Fprintln(w)
}
