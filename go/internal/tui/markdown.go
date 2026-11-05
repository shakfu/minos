package tui

// Markdown in a message body, drawn with terminal attributes.
//
// It covers what agents write: headings, emphasis, inline and fenced code,
// lists, quotes, rules and links. A line break in the source stays a line
// break, as in chat, rather than joining a paragraph. Tables and anything else
// are shown as written. A link shows its target, because a body is another
// user's text and the label alone can say anything.

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

// span is a run of text in one style.
type span struct {
	text  string
	style tcell.Style
}

// spanCells is the width of spans in terminal cells.
func spanCells(spans []span) int {
	total := 0
	for _, s := range spans {
		total += cells(s.text)
	}
	return total
}

// markdown renders text into lines of at most width cells. base styles the
// text, quiet the markers, code and link targets. Blank lines collapse as wrap
// collapses them.
func markdown(text string, width int, base, quiet tcell.Style) [][]span {
	width = max(1, width)
	var lines [][]span
	blank := false
	emit := func(rendered ...[]span) {
		if blank && len(lines) > 0 {
			lines = append(lines, nil)
		}
		blank = false
		lines = append(lines, rendered...)
	}

	fence := ""
	for _, source := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(source)
		if fence != "" {
			if strings.HasPrefix(trimmed, fence) {
				fence = ""
				continue
			}
			emit(hardWrap(strings.TrimRight(source, " \t"), width, "  ", quiet)...)
			continue
		}
		switch {
		case trimmed == "":
			blank = true
		case strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~"):
			fence = trimmed[:3]
		case isRule(trimmed):
			emit([]span{{strings.Repeat("-", min(width, 40)), quiet}})
		case strings.HasPrefix(trimmed, "|"):
			emit(hardWrap(trimmed, width, "", base)...)
		default:
			if level, title := heading(trimmed); level > 0 {
				emit(fillSpans(inline(title, base.Bold(true), quiet), width, nil, nil)...)
				continue
			}
			if rest, ok := strings.CutPrefix(trimmed, ">"); ok {
				marker := []span{{"| ", quiet}}
				emit(fillSpans(inline(strings.TrimSpace(rest), base.Italic(true), quiet), width, marker, marker)...)
				continue
			}
			indent := strings.Repeat(" ", min(8, len(source)-len(strings.TrimLeft(source, " "))))
			if marker, rest, ok := listItem(trimmed); ok {
				first := []span{{indent + marker, quiet}}
				hang := []span{{strings.Repeat(" ", cells(indent+marker)), base}}
				emit(fillSpans(inline(rest, base, quiet), width, first, hang)...)
				continue
			}
			emit(fillSpans(inline(trimmed, base, quiet), width, nil, nil)...)
		}
	}
	return lines
}

func isRule(line string) bool {
	compact := strings.ReplaceAll(line, " ", "")
	if len(compact) < 3 {
		return false
	}
	for _, mark := range "-*_" {
		if strings.Trim(compact, string(mark)) == "" {
			return true
		}
	}
	return false
}

// heading is the level and text of an ATX heading, or 0.
func heading(line string) (int, string) {
	level := len(line) - len(strings.TrimLeft(line, "#"))
	if level < 1 || level > 6 {
		return 0, ""
	}
	rest := line[level:]
	if rest != "" && rest[0] != ' ' && rest[0] != '\t' {
		return 0, ""
	}
	return level, strings.TrimSpace(strings.TrimRight(strings.TrimSpace(rest), "#"))
}

// listItem splits a bullet or numbered item into its marker, with the space
// after it, and its text.
func listItem(line string) (marker, rest string, ok bool) {
	if len(line) >= 2 && strings.ContainsRune("-*+", rune(line[0])) && line[1] == ' ' {
		return "- ", strings.TrimSpace(line[2:]), true
	}
	digits := len(line) - len(strings.TrimLeft(line, "0123456789"))
	if digits > 0 && digits <= 9 && len(line) > digits+1 &&
		(line[digits] == '.' || line[digits] == ')') && line[digits+1] == ' ' {
		return line[:digits+1] + " ", strings.TrimSpace(line[digits+2:]), true
	}
	return "", "", false
}

// inline renders emphasis, code and links in one line. An opening marker
// without its closing one is text.
func inline(text string, style, quiet tcell.Style) []span {
	var out []span
	var plainRun strings.Builder
	add := func(s span) {
		if plainRun.Len() > 0 {
			out = append(out, span{plainRun.String(), style})
			plainRun.Reset()
		}
		if s.text != "" {
			out = append(out, s)
		}
	}
	for i := 0; i < len(text); {
		rest := text[i:]
		switch {
		case rest[0] == '\\' && len(rest) > 1 && strings.ContainsRune("\\`*_~[]()#>-+.!|", rune(rest[1])):
			plainRun.WriteByte(rest[1])
			i += 2
			continue
		case rest[0] == '`':
			ticks := len(rest) - len(strings.TrimLeft(rest, "`"))
			if end := strings.Index(rest[ticks:], rest[:ticks]); end >= 0 {
				add(span{})
				out = append(out, span{strings.TrimSpace(rest[ticks : ticks+end]), quiet})
				i += 2*ticks + end
				continue
			}
		case strings.HasPrefix(rest, "**") || strings.HasPrefix(rest, "__"):
			if inner, n, ok := delimited(text, i, rest[:2]); ok {
				add(span{})
				out = append(out, inline(inner, style.Bold(true), quiet)...)
				i += n
				continue
			}
		case strings.HasPrefix(rest, "~~"):
			if inner, n, ok := delimited(text, i, "~~"); ok {
				add(span{})
				out = append(out, inline(inner, style.StrikeThrough(true), quiet)...)
				i += n
				continue
			}
		case rest[0] == '*' || rest[0] == '_':
			if inner, n, ok := delimited(text, i, rest[:1]); ok {
				add(span{})
				out = append(out, inline(inner, style.Italic(true), quiet)...)
				i += n
				continue
			}
		case rest[0] == '[':
			if label, target, n, ok := link(rest); ok {
				add(span{})
				out = append(out, inline(label, style.Underline(true), quiet)...)
				if target != label {
					out = append(out, span{" (" + target + ")", quiet})
				}
				i += n
				continue
			}
		}
		r, size := utf8.DecodeRuneInString(rest)
		plainRun.WriteRune(r)
		i += size
	}
	add(span{})
	return out
}

// delimited finds the text between mark at text[at:] and the next mark. The
// opening one must be followed by a non-space and the closing one preceded by
// one. An underscore must also sit at a word's edge, so snake_case stays text.
func delimited(text string, at int, mark string) (inner string, consumed int, ok bool) {
	start := at + len(mark)
	if start >= len(text) || text[start] == ' ' {
		return "", 0, false
	}
	if mark[0] == '_' && at > 0 && isWord(lastRune(text[:at])) {
		return "", 0, false
	}
	for from := start; ; {
		end := strings.Index(text[from:], mark)
		if end < 0 {
			return "", 0, false
		}
		end += from
		after := end + len(mark)
		// In "***" closing a double marker, the double is the last two.
		for len(mark) == 2 && after < len(text) && text[after] == mark[0] {
			end, after = end+1, after+1
		}
		closes := end > start && text[end-1] != ' '
		if mark[0] == '_' && after < len(text) && isWord(firstRune(text[after:])) {
			closes = false
		}
		// A single marker is not the start of a double one.
		if len(mark) == 1 && after < len(text) && text[after] == mark[0] {
			closes = false
		}
		if closes {
			return text[start:end], after - at, true
		}
		from = end + len(mark)
	}
}

// link parses [label](target) at the start of text.
func link(text string) (label, target string, consumed int, ok bool) {
	shut := strings.Index(text, "](")
	if shut < 1 {
		return "", "", 0, false
	}
	end := strings.IndexByte(text[shut+2:], ')')
	if end < 0 {
		return "", "", 0, false
	}
	target = strings.TrimSpace(text[shut+2 : shut+2+end])
	if target == "" || strings.ContainsAny(target, " \t") {
		return "", "", 0, false
	}
	return text[1:shut], target, shut + 3 + end, true
}

func isWord(r rune) bool      { return unicode.IsLetter(r) || unicode.IsDigit(r) }
func lastRune(s string) rune  { r, _ := utf8.DecodeLastRuneInString(s); return r }
func firstRune(s string) rune { r, _ := utf8.DecodeRuneInString(s); return r }

// fillSpans is fill for styled text: first leads the first line and hang the
// rest, and both count against width.
func fillSpans(spans []span, width int, first, hang []span) [][]span {
	var lines [][]span
	current := append([]span(nil), first...)
	lead := spanCells(first)
	used, words := lead, 0
	flush := func() {
		lines = append(lines, current)
		current = append([]span(nil), hang...)
		lead = spanCells(hang)
		used, words = lead, 0
	}
	for _, word := range splitWords(spans) {
		for len(word) > 0 {
			gap := min(words, 1)
			size := spanCells(word)
			if used+gap+size <= width {
				if gap == 1 {
					current = append(current, span{" ", word[0].style})
				}
				current = append(current, word...)
				used, words = used+gap+size, words+1
				break
			}
			if size > width-lead {
				head, tail := cutSpans(word, width-used-gap)
				if len(head) == 0 && words == 0 {
					head, tail = cutSpans(word, max(1, cells(firstCluster(word[0].text))))
				}
				if len(head) > 0 {
					if gap == 1 {
						current = append(current, span{" ", word[0].style})
					}
					current = append(current, head...)
					word = tail
				}
			}
			flush()
		}
	}
	if words > 0 || len(lines) == 0 {
		lines = append(lines, current)
	}
	return lines
}

// splitWords splits styled text at whitespace. A word may hold several styles, as
// in "**bold**," where the comma is plain.
func splitWords(spans []span) [][]span {
	var words [][]span
	var word []span
	end := func() {
		if len(word) > 0 {
			words = append(words, word)
			word = nil
		}
	}
	for _, s := range spans {
		start := -1
		for i, r := range s.text {
			if unicode.IsSpace(r) {
				if start >= 0 {
					word = append(word, span{s.text[start:i], s.style})
					start = -1
				}
				end()
			} else if start < 0 {
				start = i
			}
		}
		if start >= 0 {
			word = append(word, span{s.text[start:], s.style})
		}
	}
	end()
	return words
}

// cutSpans is cut for styled text.
func cutSpans(word []span, room int) (head, tail []span) {
	for i, s := range word {
		size := cells(s.text)
		if size <= room {
			head = append(head, s)
			room -= size
			continue
		}
		before, after := cut(s.text, room)
		if before != "" {
			head = append(head, span{before, s.style})
		}
		tail = append([]span{{after, s.style}}, word[i+1:]...)
		return head, tail
	}
	return head, nil
}

func firstCluster(text string) string {
	cluster, _, _, _ := uniseg.FirstGraphemeClusterInString(text, -1)
	return cluster
}

// hardWrap breaks text that keeps its spacing, such as code, at width cells.
func hardWrap(text string, width int, indent string, style tcell.Style) [][]span {
	room := max(1, width-cells(indent))
	var lines [][]span
	for {
		head, tail := cut(text, room)
		if head == "" && tail != "" {
			head, tail = firstCluster(tail), tail[len(firstCluster(tail)):]
		}
		lines = append(lines, []span{{indent + head, style}})
		if tail == "" {
			return lines
		}
		text = tail
	}
}
