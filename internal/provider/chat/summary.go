package chat

import (
	"strconv"
	"strings"

	"github.com/teagan42/chorus/internal/journal"
)

// maxResult bounds how much of one tool result the summary is shown: enough
// for an answer, not a search's whole catalogue.
const maxResult = 400

// Transcript is the conversation as the summary is shown it: who said what,
// what the assistant did, and what came back. Every utterance is quoted, so
// none of it reads as a line of the transcript's own.
func Transcript(dialogue []journal.Entry, people []string) string {
	var b strings.Builder
	b.WriteString("The people in this conversation: " + strings.Join(people, ", ") + ".\n")
	for _, e := range dialogue {
		switch e.Kind {
		case journal.EntryHeard:
			who := e.Speaker
			if who == "" {
				who = "someone not recognised"
			}
			b.WriteString("\n" + who + " said " + strconv.Quote(e.Text))
		case journal.EntrySaid:
			if e.Text == "" {
				continue
			}
			b.WriteString("\nthe assistant said " + strconv.Quote(e.Text))
			if a := e.Announces; a != nil {
				b.WriteString(", unasked, " + Because(a))
			}
			if e.Cut {
				b.WriteString(", and was cut off there")
			}
		case journal.EntryCall:
			b.WriteString("\nthe assistant called " + e.Tool + " with " + strconv.Quote(e.Args))
		case journal.EntryResult:
			r := e.Result
			if len(r) > maxResult {
				r = r[:maxResult] + "..."
			}
			b.WriteString("\n" + e.Tool + " ended " + e.Outcome)
			if r != "" {
				b.WriteString(": " + strconv.Quote(r))
			}
		}
	}
	return b.String()
}

// Because is why an announcement was said, in the summary's words.
func Because(a *journal.Announcement) string {
	if a.Source == "timer" {
		return "as a timer went off"
	}
	if a.RequestedBy != "" && a.FromSatellite != "" {
		return "as " + a.RequestedBy + " asked from the " + a.FromSatellite
	}
	return "as someone asked from another room"
}

// Unthought drops reasoning a model wrote into its content rather than the
// thinking field, which some do with thinking left unset.
func Unthought(content string) string {
	if i := strings.LastIndex(content, "</think>"); i >= 0 {
		content = content[i+len("</think>"):]
	}
	return strings.TrimSpace(content)
}
