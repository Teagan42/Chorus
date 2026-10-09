package main

import (
	"context"

	"github.com/teaganglenn/chorus/internal/curation"
	"github.com/teaganglenn/chorus/internal/reviewui/household"
	"github.com/teaganglenn/chorus/internal/reviewui/ui"
)

// demoNotice is what the hosted demo says above every screen.
func demoNotice() *ui.Notice {
	return &ui.Notice{
		Label:    "Demo",
		Text:     "A made-up household's Thursday, running entirely in your browser. Verdicts reset when you reload, and Replay answers from a script, not a model.",
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
	s := newServer(j, curation.NewMemStore(), blobs, household.ReviewedAt)
	s.engineFor = household.NewModel(j).Engines
	s.notice = demoNotice()
	return s, nil
}
