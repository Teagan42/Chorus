package main

import (
	"context"
	"time"

	"github.com/teagan42/chorus/internal/contexts"
	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/memory"
	"github.com/teagan42/chorus/internal/reviewui/household"
	"github.com/teagan42/chorus/internal/reviewui/ui"
)

// demoNotice is what the hosted demo says above every screen.
func demoNotice() *ui.Notice {
	return &ui.Notice{
		Label:    "Demo",
		Text:     "A made-up household's Thursday, running entirely in your browser. Verdicts and deletions reset when you reload, and Replay answers from a script, not a model.",
		LinkText: "About Chorus",
		Href:     "https://teagan42.github.io/Chorus/",
	}
}

// newDemoServer serves the household's Thursday (internal/reviewui/household)
// from memory, at the evening the reviewer sits down, with Replay asking the
// household's scripted model. It needs no database, no disk and no network,
// which is what lets it run as WebAssembly on a static site.
func newDemoServer(ctx context.Context) (*server, error) {
	j, err := household.Journal(ctx)
	if err != nil {
		return nil, err
	}
	blobs, err := household.Blobs(ctx)
	if err != nil {
		return nil, err
	}
	memories, err := demoMemories(ctx)
	if err != nil {
		return nil, err
	}
	s := newServer(j, curation.NewMemStore(), blobs, household.ReviewedAt)
	s.engineFor = household.NewModel(j).Engines
	s.contexts = contexts.Chain{contexts.Default(), household.Contexts()}
	s.notice = demoNotice()
	s.memories = memories
	return s, nil
}

// demoMemories is what the household's turns were told it remembers: the
// book club facts, kept the week before, and the night before's summary.
func demoMemories(ctx context.Context) (*memory.MemStore, error) {
	m := memory.NewMemStore()
	weekBefore := household.Day().Add(-7 * 24 * time.Hour)
	for i, r := range household.BookClub() {
		err := m.Remember(ctx, memory.Memory{
			ID: r.ID, Person: r.Person, Fact: r.Fact, Shareable: r.Shareable,
			ConversationID: "conv-week-before-" + r.Person, CallID: "call_remember",
			At: weekBefore.Add(time.Duration(i) * time.Hour),
		})
		if err != nil {
			return nil, err
		}
	}
	for _, sum := range household.LastNight() {
		if err := m.Summarized(ctx, memory.Summary{ConversationID: sum.ConversationID, Person: "teagan", Text: sum.Text, At: sum.At}); err != nil {
			return nil, err
		}
	}
	return m, nil
}
