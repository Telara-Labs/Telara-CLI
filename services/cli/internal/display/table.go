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
// Headers are rendered bold and the separator row uses dim ─ characters.
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

	// Bold headers — padding must happen before applying ANSI codes.
	printStyledRow(w, t.Headers, widths, ColorBold.SprintFunc())

	// Dim separator using ─ (U+2500). Each ─ is one display column.
	for i, colW := range widths {
		if i > 0 {
			fmt.Fprint(w, "  ")
		}
		fmt.Fprint(w, ColorDim.Sprint(strings.Repeat(IconDash, colW)))
	}
	fmt.Fprintln(w)

	for _, row := range t.Rows {
		printRow(w, row, widths)
	}
}

// printStyledRow renders a row applying styleFn to each fixed-width cell.
// Padding is computed from the raw string BEFORE styling to avoid ANSI code
// length interference.
func printStyledRow(w io.Writer, cols []string, widths []int, styleFn func(a ...interface{}) string) {
	for i, col := range cols {
		if i > 0 {
			fmt.Fprint(w, "  ")
		}
		if i < len(widths) {
			padded := fmt.Sprintf("%-*s", widths[i], col)
			fmt.Fprint(w, styleFn(padded))
		} else {
			fmt.Fprint(w, styleFn(col))
		}
	}
	fmt.Fprintln(w)
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
