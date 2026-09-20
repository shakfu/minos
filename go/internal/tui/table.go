package tui

// Rows in aligned columns, for the list screens.
//
// Widths are terminal cells, so a character two cells wide takes two. A column
// is as wide as its widest cell and is cut in shrink order until the row fits;
// one cut to nothing is left out, title and all, which is how a narrow terminal
// loses the least useful column rather than the last one.

import (
	"strings"

	"github.com/rivo/uniseg"
)

// Every row starts with a one-cell gutter holding the selection marker, and
// columnGap separates the columns.
const (
	gutter    = "> "
	columnGap = "  "
)

// column describes one column of a table.
type column struct {
	title string
	right bool // aligned right

	// shrink orders the columns cut when a row does not fit: highest first,
	// and never when zero. min is the narrowest a column is cut to.
	shrink, min int
}

type table struct {
	cols   []column
	rows   [][]string
	widths []int
}

func (t *table) add(cells ...string) { t.rows = append(t.rows, cells) }

// layout sizes the columns for width.
func (t *table) layout(width int) {
	width = max(1, width-len(gutter))
	t.widths = make([]int, len(t.cols))
	for _, row := range t.rows {
		for index, cell := range row {
			if index < len(t.cols) {
				t.widths[index] = max(t.widths[index], cells(cell))
			}
		}
	}
	for index, col := range t.cols {
		if t.widths[index] > 0 {
			t.widths[index] = max(t.widths[index], cells(col.title))
		}
	}
	for t.total() > width {
		cut := -1
		for index, col := range t.cols {
			if col.shrink > 0 && t.widths[index] > col.min &&
				(cut < 0 || col.shrink > t.cols[cut].shrink) {
				cut = index
			}
		}
		if cut < 0 {
			return
		}
		t.widths[cut] = max(t.cols[cut].min, t.widths[cut]-(t.total()-width))
	}
}

// total is the width of a row: the shown columns and the gaps between them.
func (t *table) total() int {
	width, shown := 0, 0
	for _, w := range t.widths {
		if w > 0 {
			width += w
			shown++
		}
	}
	return width + len(columnGap)*max(0, shown-1)
}

// header is the column titles.
func (t *table) header() string {
	titles := make([]string, len(t.cols))
	for index, col := range t.cols {
		titles[index] = col.title
	}
	return t.draw(titles, false)
}

// line is one row, marked when it is the one under the cursor.
func (t *table) line(row int, marked bool) string {
	if row < 0 || row >= len(t.rows) {
		return ""
	}
	return t.draw(t.rows[row], marked)
}

func (t *table) draw(row []string, marked bool) string {
	var text strings.Builder
	if marked {
		text.WriteString(gutter)
	} else {
		text.WriteString(strings.Repeat(" ", len(gutter)))
	}
	first := true
	for index, width := range t.widths {
		if width == 0 {
			continue
		}
		if !first {
			text.WriteString(columnGap)
		}
		first = false
		cell := ""
		if index < len(row) {
			cell, _ = cut(printable(row[index]), width)
		}
		space := strings.Repeat(" ", width-cells(cell))
		if t.cols[index].right {
			text.WriteString(space + cell)
		} else {
			text.WriteString(cell + space)
		}
	}
	return strings.TrimRight(text.String(), " ")
}

func cells(text string) int { return uniseg.StringWidth(printable(text)) }
