package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"

	"minos/internal/client"
	"minos/internal/config"
	"minos/internal/testserver"
)

var quietStyle = plain.Dim(true)

// rendered is markdown's lines as text, styles dropped.
func rendered(text string, width int) []string {
	var out []string
	for _, spans := range markdown(text, width, plain, quietStyle) {
		var line strings.Builder
		for _, s := range spans {
			line.WriteString(s.text)
		}
		out = append(out, line.String())
	}
	return out
}

// styleOf is the style of the span holding the first word of text, and
// whether there is one. Wrapping splits spans at spaces.
func styleOf(text, within string) (tcell.Style, bool) {
	first := strings.Fields(text)[0]
	for _, spans := range markdown(within, 80, plain, quietStyle) {
		for _, s := range spans {
			if s.text == first {
				return s.style, true
			}
		}
	}
	return plain, false
}

func TestMarkdownLaysOutBlocks(t *testing.T) {
	for _, case_ := range []struct {
		text  string
		width int
		want  []string
	}{
		{"# Title #\nbody", 20, []string{"Title", "body"}},
		{"- one two three\n- four", 9, []string{"- one two", "  three", "- four"}},
		{"* star\n  + nested", 20, []string{"- star", "  - nested"}},
		{"1. first item\n10) tenth", 9, []string{"1. first", "   item", "10) tenth"}},
		{"> quoted words here", 10, []string{"| quoted", "| words", "| here"}},
		{"a\n\n\n---\nb", 5, []string{"a", "", "-----", "b"}},
		// Code keeps its spacing, is indented, and breaks at the width.
		{"```go\nif  x {\n\treturn\n}\n```\nafter", 20, []string{"  if  x {", "  \treturn", "  }", "after"}},
		{"```\nabcdefgh\n```", 6, []string{"  abcd", "  efgh"}},
		// An unclosed fence runs to the end.
		{"```\n**raw**", 20, []string{"  **raw**"}},
		{"| a | b |\n|---|---|", 40, []string{"| a | b |", "|---|---|"}},
		// A line break in the source stays one, as in chat.
		{"line one\nline two", 40, []string{"line one", "line two"}},
		{"", 10, nil},
		{"\n \n", 10, nil},
		{"#hashtag", 20, []string{"#hashtag"}},
	} {
		if got := rendered(case_.text, case_.width); !slices.Equal(got, case_.want) {
			t.Errorf("markdown(%q, %d) = %q, want %q", case_.text, case_.width, got, case_.want)
		}
	}
}

func TestMarkdownStylesInlineMarkup(t *testing.T) {
	for _, case_ := range []struct {
		text, span string
		want       tcell.Style
	}{
		{"a **bold** b", "bold", plain.Bold(true)},
		{"a __bold__ b", "bold", plain.Bold(true)},
		{"an *em* b", "em", plain.Italic(true)},
		{"an _em_ b", "em", plain.Italic(true)},
		{"**bold *both***", "both", plain.Bold(true).Italic(true)},
		{"~~gone~~", "gone", plain.StrikeThrough(true)},
		{"run `go test`", "go test", quietStyle},
		{"``a ` b``", "a ` b", quietStyle},
		{"## big", "big", plain.Bold(true)},
		{"see [docs](https://x.test/d)", "docs", plain.Underline(true)},
		{"see [docs](https://x.test/d)", " (https://x.test/d)", quietStyle},
	} {
		got, ok := styleOf(case_.span, case_.text)
		if !ok {
			t.Errorf("%q: no span %q in %q", case_.text, case_.span, rendered(case_.text, 80))
			continue
		}
		if got != case_.want {
			t.Errorf("%q: %q styled %v, want %v", case_.text, case_.span, got, case_.want)
		}
	}
}

// What is not markup is shown as written.
func TestMarkdownLeavesTextThatIsNotMarkup(t *testing.T) {
	for _, text := range []string{
		"snake_case_name",
		"2 * 3 * 4",
		"**unclosed",
		"a ** b ** c",
		"[not a link] (x)",
		"[label](has space)",
		"`open",
		"price_usd and total_cost",
	} {
		if got := rendered(text, 80); !slices.Equal(got, []string{text}) {
			t.Errorf("markdown(%q) = %q", text, got)
		}
	}
	if got := rendered(`\*literal\*`, 80); !slices.Equal(got, []string{"*literal*"}) {
		t.Errorf("an escape renders %q", got)
	}
}

// A styled word wraps whole, punctuation with it, and a word wider than the
// line breaks inside it.
func TestMarkdownWrapsStyledWords(t *testing.T) {
	if got := rendered("aa **bbb**, cc", 6); !slices.Equal(got, []string{"aa", "bbb,", "cc"}) {
		t.Errorf("wrapped %q", got)
	}
	if got := rendered("- **abcdefgh**", 6); !slices.Equal(got, []string{"- abcd", "  efgh"}) {
		t.Errorf("wrapped %q", got)
	}
	if got := rendered("日本語です", 5); !slices.Equal(got, []string{"日本", "語で", "す"}) {
		t.Errorf("wrapped %q", got)
	}
	for _, line := range markdown("- **"+strings.Repeat("word ", 40)+"**", 17, plain, quietStyle) {
		if width := spanCells(line); width > 17 {
			t.Fatalf("a line of %d cells in 17", width)
		}
	}
}

// A message in a room is drawn as markdown, with its markers gone and its
// emphasis kept, and an escape sequence in it is still shown, not obeyed.
func TestARoomDrawsAMessageAsMarkdown(t *testing.T) {
	server := testserver.Start(t, config.HistoryLimit)
	demo, alice := connect(t, server.Base, "demo"), connect(t, server.Base, "alice")
	screen := simulated(t, 80, 24)
	ui := newUi(screen, demo, client.Profile{Username: "demo"})
	ui.command("/open alice")
	room := ui.selected
	waitFor(t, "alice's invitation", func() bool { _, ok := alice.Space(room); return ok })
	if err := alice.Send(room, "## Findings\n- **two** races in `broker`\x1b[2J"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the message", func() bool { return len(demo.Log(room)) == 1 })

	ui.ensureSelection()
	ui.draw()
	drawn := contents(screen)
	for _, want := range []string{"alice      Findings", "- two races in broker?[2J"} {
		if !strings.Contains(drawn, want) {
			t.Errorf("the room lacks %q:\n%s", want, drawn)
		}
	}
	if strings.Contains(drawn, "**") || strings.Contains(drawn, "##") || strings.ContainsRune(drawn, '\x1b') {
		t.Errorf("markup is drawn raw:\n%s", drawn)
	}
	x, y := cellOf(t, screen, "two")
	cells, width, _ := screen.GetContents()
	if _, _, attrs := cells[y*width+x].Style.Decompose(); attrs&tcell.AttrBold == 0 {
		t.Errorf("**two** is not bold")
	}
}

// cellOf is where text starts on the screen.
func cellOf(t *testing.T, screen tcell.SimulationScreen, text string) (int, int) {
	t.Helper()
	_, width, _ := screen.GetContents()
	for y, row := range strings.Split(contents(screen), "\n") {
		if x := strings.Index(row, " "+text+" "); x >= 0 && len([]rune(row[:x])) == x {
			return x + 1, y
		}
	}
	t.Fatalf("no %q on a %d-wide screen:\n%s", text, width, contents(screen))
	return 0, 0
}
