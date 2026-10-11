package main

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/teagan42/chorus/internal/erasure"
	"github.com/teagan42/chorus/internal/memory"
	"github.com/teagan42/chorus/internal/reviewui/ui"
)

// Memories is the memory store as the memory screen reads and forgets it.
type Memories interface {
	memory.Store
	memory.Kept
}

// redaction is what deleting and forgetting need beside the journal.
type redaction struct {
	// memories is nil on an instance that keeps none; the screen says so.
	memories Memories
	// erasures is the trail of what a reviewer deleted (ADR-0070).
	erasures erasure.Store
	// readOnly hides and refuses every deletion and forgetting.
	readOnly bool
}

// whenKept is how the screens date a memory, a summary or a deletion.
const whenKept = "Mon 2 Jan 15:04"

// memoryFact is one remembered fact as the screen lists it.
type memoryFact struct {
	Anchor   string
	Fact     string
	Shared   ui.SigTag
	Kept     string
	From     string
	FromHref string
	Forget   *ui.Confirm
}

// memorySummary is one conversation's summary kept for one person.
type memorySummary struct {
	Anchor   string
	Text     string
	Tag      ui.SigTag
	Kept     string
	From     string
	FromHref string
	Forget   *ui.Confirm
}

// memoryPerson is everything kept about one person.
type memoryPerson struct {
	Person    string
	Facts     []memoryFact
	Summaries []memorySummary
}

// memoryList is the screen's body, which a forget swaps whole.
type memoryList struct {
	People []memoryPerson
	Empty  *ui.EmptyState
	Note   string
}

// from names the conversation a memory came from, linked while its log is
// still in the journal.
func from(id string, listed map[string]bool) (string, string) {
	if listed[id] {
		return id, conversationHref(id)
	}
	return id + " · no longer in the journal", ""
}

func factAnchor(m memory.Memory) string { return "fact-" + m.ID }

func summaryAnchor(s memory.Summary) string { return "summary-" + s.Person + "-" + s.ConversationID }

func (s *server) memoryList(ctx context.Context) (memoryList, error) {
	var v memoryList
	if s.memories == nil {
		v.Empty = &ui.EmptyState{Title: "This review UI keeps no memories.", Body: "It was started without the memory store chorusd remembers into."}
		return v, nil
	}
	ms, err := s.memories.AllMemories(ctx)
	if err != nil {
		return v, err
	}
	ss, err := s.memories.AllSummaries(ctx)
	if err != nil {
		return v, err
	}
	ids, err := s.journal.Conversations(ctx)
	if err != nil {
		return v, err
	}
	listed := map[string]bool{}
	for _, id := range ids {
		listed[id] = true
	}
	loc := s.now().Location()
	byPerson := map[string]*memoryPerson{}
	person := func(p string) *memoryPerson {
		if byPerson[p] == nil {
			byPerson[p] = &memoryPerson{Person: p}
		}
		return byPerson[p]
	}
	for _, m := range ms {
		f := memoryFact{Anchor: factAnchor(m), Fact: m.Fact, Kept: m.At.In(loc).Format(whenKept)}
		f.From, f.FromHref = from(m.ConversationID, listed)
		f.Shared = ui.SigTag{Text: "private to " + m.Person, Tone: ui.ToneMuted}
		if m.Shareable {
			f.Shared = ui.SigTag{Text: "shared with the household", Tone: ui.TonePeople}
		}
		if !s.readOnly {
			f.Forget = &ui.Confirm{
				ID: "forget-" + f.Anchor, Label: "Forget…", Tone: ui.ToneHome,
				Title: "Forget this about " + m.Person + "?",
				Body: "The assistant stops recalling it from its next turn, and nothing keeps a copy of the fact. " +
					"The remember call that kept it stays in its conversation's log as history; delete that conversation to remove it there too.",
				Action:  memoryRoute + "/" + url.PathEscape(m.Person) + "/facts/" + url.PathEscape(m.ID) + "/forget",
				Confirm: "forget", Button: "Forget it", Target: "#memory", Swap: "outerHTML",
			}
		}
		p := person(m.Person)
		p.Facts = append(p.Facts, f)
	}
	for _, sum := range ss {
		row := memorySummary{
			Anchor: summaryAnchor(sum), Text: sum.Text, Kept: sum.At.In(loc).Format(whenKept),
			Tag: ui.SigTag{Text: "private to " + sum.Person, Tone: ui.ToneMuted},
		}
		row.From, row.FromHref = from(sum.ConversationID, listed)
		if !s.readOnly {
			row.Forget = &ui.Confirm{
				ID: "forget-" + row.Anchor, Label: "Forget…", Tone: ui.ToneHome,
				Title:   "Forget what " + sum.Person + " was told of this conversation?",
				Body:    "No later turn of " + sum.Person + "'s is told this summary again, and nothing keeps a copy of it. The conversation itself stays.",
				Action:  memoryRoute + "/" + url.PathEscape(sum.Person) + "/summaries/" + url.PathEscape(sum.ConversationID) + "/forget",
				Confirm: "forget", Button: "Forget it", Target: "#memory", Swap: "outerHTML",
			}
		}
		p := person(sum.Person)
		p.Summaries = append(p.Summaries, row)
	}
	for _, p := range byPerson {
		v.People = append(v.People, *p)
	}
	slices.SortFunc(v.People, func(a, b memoryPerson) int { return cmp.Compare(a.Person, b.Person) })
	if len(v.People) == 0 {
		v.Empty = &ui.EmptyState{Title: "Nothing is remembered.", Body: "Facts land here when someone asks the assistant to remember something, and summaries when a conversation ends."}
	}
	if s.readOnly {
		v.Note = "Read-only: this review UI forgets nothing."
	}
	return v, nil
}

// memoryPage lists what the assistant remembers, per person (SPEC §5).
func (s *server) memoryPage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.memoryList(r.Context())
	if err != nil {
		http.Error(w, "The memory store would not read: "+err.Error(), http.StatusInternalServerError)
		return
	}
	pairs, err := s.pairs(r.Context(), nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.render(w, "page-memory", map[string]any{
		"Doc":    s.doc("Memory"),
		"Header": ui.NewAppHeader("", unreviewedCount(pairs), "chorus · journal"),
		"Head": ui.PageHead{
			Eyebrow: "Memory", Title: "What it remembers",
			Subtitle: "Facts each person asked to be remembered, and the summaries of their conversations, as the assistant recalls them (SPEC §5).",
			Actions:  []ui.Button{{Label: "Deletions ›", Href: deletionsRoute}},
		},
		"List": list,
	})
}

// forgetFact forgets one remembered fact through the memory store's own
// Forget, which a person's forget tool also calls (ADR-0040).
func (s *server) forgetFact(w http.ResponseWriter, r *http.Request) {
	person, id := r.PathValue("person"), r.PathValue("fact")
	s.forget(w, r, func(ctx context.Context) (erasure.Record, bool, error) {
		var conv string
		if ms, err := s.memories.AllMemories(ctx); err == nil {
			for _, m := range ms {
				if m.ID == id && m.Person == person {
					conv = m.ConversationID
				}
			}
		}
		ok, err := s.memories.Forget(ctx, person, id)
		return erasure.Record{Kind: erasure.KindMemory, ConversationID: conv, Subject: id, Person: person}, ok, err
	}, fmt.Sprintf("Nothing of %s's is remembered as %s, so there is nothing to forget. It may be forgotten already: reload the page.", person, id))
}

// forgetSummary forgets one conversation's summary for one person.
func (s *server) forgetSummary(w http.ResponseWriter, r *http.Request) {
	person, conv := r.PathValue("person"), r.PathValue("conv")
	s.forget(w, r, func(ctx context.Context) (erasure.Record, bool, error) {
		ok, err := s.memories.ForgetSummary(ctx, conv, person)
		return erasure.Record{Kind: erasure.KindSummary, ConversationID: conv, Person: person}, ok, err
	}, fmt.Sprintf("No summary of %s is kept for %s, so there is nothing to forget. It may be forgotten already: reload the page.", conv, person))
}

// forget runs one forgetting, records it, and answers with the screen's
// body read again.
func (s *server) forget(w http.ResponseWriter, r *http.Request, do func(context.Context) (erasure.Record, bool, error), absent string) {
	if s.readOnly {
		http.Error(w, readOnlyRefusal, http.StatusForbidden)
		return
	}
	if !confirmed(w, r, "forget") {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.memories == nil {
		http.Error(w, "This review UI keeps no memories, so there is nothing to forget.", http.StatusNotFound)
		return
	}
	rec, ok, err := do(r.Context())
	switch {
	case err != nil:
		http.Error(w, "Nothing was forgotten: the memory store said "+err.Error(), http.StatusInternalServerError)
		return
	case !ok:
		http.Error(w, absent, http.StatusNotFound)
		return
	}
	if rec.ConversationID == "" {
		rec.ConversationID = "unknown"
	}
	if !s.record(w, rec) {
		return
	}
	list, err := s.memoryList(r.Context())
	if err != nil {
		http.Error(w, "Forgotten, but the memory store would not read again: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, memoryRoute, http.StatusSeeOther)
		return
	}
	s.render(w, "memory-list", list)
}

// erasedRow is one line of the deletion trail.
type erasedRow struct {
	When    string
	Kind    ui.SigTag
	What    string
	Counted string
	Why     string
}

// erasedWhy says what went and why it cannot come back, by kind.
var erasedWhy = map[erasure.Kind]string{
	erasure.KindConversation: "Its log, audio, verdicts, labels, re-runs and summaries were deleted. Nothing keeps a copy, so it cannot be restored: this line is all that is left of it.",
	erasure.KindAudio:        "The recording was removed and the log records it as redacted. The words stay; the audio cannot be restored.",
	erasure.KindMemory:       "No turn is told it again, and nothing keeps a copy of the fact. The remember call is still in its conversation's log unless that was deleted too.",
	erasure.KindSummary:      "No turn is told it again, and nothing keeps a copy of the summary.",
}

var erasedTone = map[erasure.Kind]ui.Tone{
	erasure.KindConversation: ui.ToneConv, erasure.KindAudio: ui.ToneVoice,
	erasure.KindMemory: ui.TonePeople, erasure.KindSummary: ui.TonePeople,
}

// deletions is the trail: what a reviewer deleted, newest first, named by
// id and counted, never what it held (ADR-0070).
func (s *server) deletions(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.erasures.List(r.Context())
	if err != nil {
		http.Error(w, "The record of deletions would not read: "+err.Error(), http.StatusInternalServerError)
		return
	}
	pairs, err := s.pairs(r.Context(), nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	loc := s.now().Location()
	rows := make([]erasedRow, 0, len(records))
	for _, rec := range records {
		row := erasedRow{
			When: rec.At.In(loc).Format(whenKept), Kind: ui.SigTag{Text: string(rec.Kind), Tone: erasedTone[rec.Kind]},
			Counted: rec.Counted, Why: erasedWhy[rec.Kind],
		}
		switch rec.Kind {
		case erasure.KindConversation:
			row.What = rec.ConversationID
		case erasure.KindAudio:
			row.What = rec.ConversationID + " · " + strings.ReplaceAll(rec.Subject, " ", ", ")
		case erasure.KindMemory:
			row.What = rec.Person + "'s " + rec.Subject + " · from " + rec.ConversationID
		case erasure.KindSummary:
			row.What = rec.Person + "'s summary of " + rec.ConversationID
		}
		rows = append(rows, row)
	}
	var empty *ui.EmptyState
	if len(rows) == 0 {
		empty = &ui.EmptyState{Title: "Nothing has been deleted.", Body: "A conversation deleted, a clip dropped or a memory forgotten here leaves a line on this page."}
	}
	s.render(w, "page-deletions", map[string]any{
		"Doc":    s.doc("Deletions"),
		"Header": ui.NewAppHeader("", unreviewedCount(pairs), "chorus · journal"),
		"Head": ui.PageHead{
			Eyebrow: "Deletions", Title: "What was deleted",
			Subtitle: "Every conversation deleted, clip dropped and memory forgotten in this review UI, newest first. Each line names what went, never what it held: there is no copy to restore from.",
			Actions:  []ui.Button{{Label: "Memory ›", Href: memoryRoute}},
		},
		"Rows":  rows,
		"Empty": empty,
	})
}

// deletedAlert is Browse's word that a conversation just went, when the
// trail's newest line says so.
func (s *server) deletedAlert(ctx context.Context, id string) *ui.Alert {
	if id == "" {
		return nil
	}
	records, err := s.erasures.List(ctx)
	if err != nil || len(records) == 0 || records[0].Kind != erasure.KindConversation || records[0].ConversationID != id {
		return nil
	}
	return &ui.Alert{
		Title: "Deleted " + id + " for good.",
		Body:  "Gone at " + records[0].At.In(s.now().Location()).Format("15:04") + ": " + records[0].Counted + ". Nothing keeps a copy, so it cannot be restored; Deletions keeps a line saying it went.",
		Tone:  ui.ToneConv, Actions: []ui.Button{{Label: "Deletions ›", Href: deletionsRoute}},
	}
}
