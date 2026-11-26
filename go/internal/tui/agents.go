package tui

// Who is speaking, what the agents in a room are doing, and what waits on a
// decision.
//
// Three things a room shows besides its log. Each message is marked with its
// author's kind, so an agent's text is never taken for a person's decision or
// a fact the server reports (design.md D11). A run's state sits under the
// room's header, and a message the run has not been handed yet says so. And a
// submission raised from the room, or waiting in a channel this user
// moderates, is drawn as a card that is decided where it is shown.

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"minos/internal/client"
)

// Marks for an author's kind. A glyph and, where there is room, a word: never
// colour alone.
const (
	markAgent  = "◆"
	markPerson = "●"
	markSystem = "■"
)

// authorKind is a message's author kind, guessed as the server would for a
// message from a server that predates kinds.
func authorKind(message client.Message) string {
	switch {
	case message.AuthorKind != "":
		return message.AuthorKind
	case message.Kind == "event":
		return "system"
	}
	return "user"
}

func markOf(kind string) string {
	switch kind {
	case "agent":
		return markAgent
	case "system":
		return markSystem
	}
	return markPerson
}

// -- agent state --------------------------------------------------------------

// live is the runs in a room that have not stopped.
func live(states []client.AgentState) []client.AgentState {
	return slices.DeleteFunc(slices.Clone(states), func(s client.AgentState) bool { return s.State == "stopped" })
}

// describeAgent is one run's state as a line: what it is doing, for how
// long, and what waits for it.
func (u *Ui) describeAgent(state client.AgentState) string {
	parts := []string{state.State}
	switch state.State {
	case "working", "interrupting":
		if state.Turn > 0 {
			parts = append(parts, fmt.Sprintf("turn %d", state.Turn))
		}
		parts = append(parts, u.lasted(state.Since))
	case "idle":
		parts = append(parts, "waiting for the room "+u.lasted(state.Since))
	case "stopped":
		if state.Detail != "" {
			parts[0] += " (" + state.Detail + ")"
		}
		parts = append(parts, u.ago(state.Since))
	default:
		if state.Detail != "" {
			parts = append(parts, state.Detail)
		}
	}
	if state.Held > 0 {
		parts = append(parts, plural(int(state.Held), "message")+" held for the next turn")
	}
	return fmt.Sprintf("%s %s  %s", markAgent, state.Username, strings.Join(parts, " · "))
}

// lasted is how long since a moment, as a duration.
func (u *Ui) lasted(since float64) string {
	if since <= 0 {
		return ""
	}
	d := u.now().Sub(time.Unix(0, int64(since*float64(time.Second))))
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", max(0, int(d.Seconds())))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// agentStyle colours a run's line by what it is doing.
func (u *Ui) agentStyle(state client.AgentState) tcell.Style {
	switch state.State {
	case "working", "interrupting":
		return u.tint(plain, "good")
	case "stopped":
		return u.tint(plain, "dim")
	}
	return u.tint(plain, "agent")
}

// agentLines is the room header's account of its runs: one line each, and a
// hint when one is working that it can be interrupted.
func (u *Ui) agentLines(space client.Room) []line {
	states := u.client.Agents(space.ID)
	var lines []line
	working := false
	for _, state := range states {
		lines = append(lines, line{u.describeAgent(state), u.agentStyle(state), nil})
		working = working || state.State == "working"
	}
	if working {
		lines = append(lines, line{"  ^X /interrupt [correction] stops the turn", u.tint(plain, "dim"), nil})
	}
	return lines
}

// deliveryNote says what became of a message this user sent to a room with a
// run in it: held until the turn ends, not yet handed over, or never to be.
// Empty once the run has it, but for the newest such message, which says so.
func (u *Ui) deliveryNote(message client.Message, states []client.AgentState, newest int64) string {
	var notes []string
	for _, state := range states {
		switch {
		case message.Seq <= state.Through:
			if message.Seq == newest {
				notes = append(notes, "✓ "+state.Username+" has it")
			}
		case state.State == "stopped":
			notes = append(notes, "not delivered: "+state.Username+" has stopped")
		case state.State == "working" || state.State == "interrupting":
			notes = append(notes, "held: "+state.Username+" gets it when this turn ends")
		case state.State == "closing":
			notes = append(notes, "not delivered: "+state.Username+" is closing")
		default:
			notes = append(notes, "sent to "+state.Username)
		}
	}
	return strings.Join(notes, "; ")
}

// -- decision cards ----------------------------------------------------------

// card is one submission drawn where it can be decided, or watched.
type card struct {
	submission client.Submission
	// decides is whether this user may decide it: a moderator's card.
	decides bool
}

// cards is what waits on a decision in a space: in a room, what was raised
// from it; in a channel, its queue. A moderator's first, oldest first.
func (u *Ui) cards(space client.Room) []card {
	var found []card
	belongs := func(submission client.Submission) bool {
		if space.Kind == "channel" {
			return submission.Channel == space.ID
		}
		return submission.Room == space.ID
	}
	for _, submission := range byAge(u.client.Queued()) {
		if submission.State == "pending" && belongs(submission) {
			found = append(found, card{submission, true})
		}
	}
	if space.Kind != "channel" {
		for _, submission := range byAge(u.client.Submissions()) {
			if submission.State == "pending" && belongs(submission) {
				found = append(found, card{submission, false})
			}
		}
	}
	return found
}

// focusedCard is the card ^Y and ^R act on.
func (u *Ui) focusedCard() (card, bool) {
	space, ok := u.client.Space(u.selected)
	if !ok {
		return card{}, false
	}
	var deciding []card
	for _, c := range u.cards(space) {
		if c.decides {
			deciding = append(deciding, c)
		}
	}
	if len(deciding) == 0 {
		return card{}, false
	}
	u.card = max(0, min(u.card, len(deciding)-1))
	return deciding[u.card], true
}

// cycleCard moves the focus to the next card that can be decided here.
func (u *Ui) cycleCard(step int) {
	space, ok := u.client.Space(u.selected)
	if !ok {
		return
	}
	count := 0
	for _, c := range u.cards(space) {
		if c.decides {
			count++
		}
	}
	if count > 0 {
		u.card = ((u.card+step)%count + count) % count
	}
}

// decide approves or rejects the focused card. A rejection's reason is what
// is typed in the composer, which it takes.
func (u *Ui) decide(approve bool) {
	if u.view != viewSpace {
		return
	}
	focused, ok := u.focusedCard()
	if !ok {
		u.notice("Nothing here waits on your decision")
		return
	}
	submission := focused.submission
	if approve {
		if err := u.await(func() error { return u.client.Approve(submission.ID) }); err != nil {
			u.notice(err.Error())
			return
		}
		u.notice(fmt.Sprintf("Approved %s's request: %s", submission.Author, submission.Subject))
		return
	}
	reason := strings.TrimSpace(string(u.input))
	if err := u.await(func() error { return u.client.Reject(submission.ID, reason) }); err != nil {
		u.notice(err.Error())
		return
	}
	u.input = nil
	u.notice(fmt.Sprintf("Rejected %s's request: %s", submission.Author, submission.Subject))
}

// cardLines draws the cards of a space, the focused one marked, each with
// what can be done to it.
func (u *Ui) cardLines(space client.Room, pane int) []line {
	cards := u.cards(space)
	if len(cards) == 0 {
		return nil
	}
	dim := u.tint(plain, "dim")
	edge := u.tint(plain, "decide")
	width := max(20, min(pane-2, 72))
	// A moderator need not subscribe, and a channel this client does not
	// hold has no title to show.
	where := func(id string) string {
		if found, ok := u.client.Space(id); ok {
			return " → " + found.Title
		}
		return ""
	}

	var lines []line
	index := 0
	focused, _ := u.focusedCard()
	for _, c := range cards {
		submission := c.submission
		style, title := dim, "Waiting for a moderator"
		if c.decides {
			style, title = edge, "Decision needed"
		}
		marker := "┌"
		if c.decides && submission.ID == focused.submission.ID {
			marker = "▶"
			style = style.Bold(true)
		}
		head := fmt.Sprintf("%s %s · %s%s · %s ", marker, title, submission.Author,
			where(submission.Channel), u.ago(submission.At))
		lines = append(lines, line{ljust(head, width), style, nil})
		lines = append(lines, line{"│ " + submission.Subject, plain.Bold(true), nil})
		body := strings.TrimSpace(submission.Body)
		if body != "" && body != submission.Subject {
			rendered := markdown(body, max(10, width-4), plain, dim)
			shown := min(len(rendered), cardBodyLines)
			for _, part := range rendered[:shown] {
				lines = append(lines, line{style: plain, spans: append([]span{{"│   ", style}}, part...)})
			}
			if len(rendered) > shown {
				lines = append(lines, line{fmt.Sprintf("│   (%d more lines: /queue shows it whole)", len(rendered)-shown), dim, nil})
			}
		}
		if c.decides {
			lines = append(lines, line{"│ ^Y approve   ^R reject (what is typed is the reason)   ^F next", dim, nil})
			index++
		} else {
			lines = append(lines, line{"│ you asked; a moderator decides", dim, nil})
		}
		lines = append(lines, line{"└" + strings.Repeat("─", max(0, width-1)), style.Bold(false), nil})
	}
	return lines
}

// cardBodyLines is the most of a submission's body a card shows.
const cardBodyLines = 6

// -- progress ----------------------------------------------------------------

// foldProgress is how a run of progress lines is shown when folded: one line
// counting them, with the newest.
func foldProgress(run []client.Message) string {
	last, _, _ := strings.Cut(strings.TrimSpace(run[len(run)-1].Body), "\n")
	return fmt.Sprintf("▸ %s  (latest: %s)  ^E shows them", plural(len(run), "progress line"), last)
}
