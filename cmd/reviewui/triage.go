package main

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"

	"github.com/teaganglenn/chorus/internal/reviewui/ui"
	"github.com/teaganglenn/chorus/internal/triage"
)

// triageTabs are the signal filters, in the order the mock lists them.
var triageTabs = []struct {
	id, label string
	kind      triage.Kind
}{
	{"all", "All", ""},
	{string(triage.KindBargeIn), "Barge-in pairs", triage.KindBargeIn},
	{string(triage.KindRepeated), "Repeated", triage.KindRepeated},
	{string(triage.KindSlow), "Slow", triage.KindSlow},
	{string(triage.KindFailure), "Failures", triage.KindFailure},
	{string(triage.KindSpeakerFlip), "Speaker flips", triage.KindSpeakerFlip},
}

var signalTags = map[triage.Kind]ui.SigTag{
	triage.KindBargeIn:     {Text: "barge-in pair", Tone: ui.TonePeople},
	triage.KindFailure:     {Text: "failure", Tone: ui.ToneHome},
	triage.KindRepeated:    {Text: "repeated", Tone: ui.ToneConv},
	triage.KindSlow:        {Text: "slow", Tone: ui.ToneVoice},
	triage.KindSpeakerFlip: {Text: "speaker flip", Tone: ui.TonePeople},
}

// signals scans every conversation, newest event first across the household,
// and counts the unreviewed pairs the header badges on every page.
func (s *server) signals(r *http.Request) ([]triage.Signal, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pairs, err := s.pairs(r.Context())
	if err != nil {
		return nil, 0, err
	}
	convs, err := s.journal.Conversations(r.Context())
	if err != nil {
		return nil, 0, err
	}
	var out []triage.Signal
	for _, conv := range convs {
		sigs, err := triage.Scan(r.Context(), s.journal, conv)
		if err != nil {
			return nil, 0, fmt.Errorf("triage %s: %w", conv, err)
		}
		out = append(out, sigs...)
	}
	slices.SortStableFunc(out, func(a, b triage.Signal) int {
		if c := b.At.Compare(a.At); c != 0 {
			return c
		}
		return cmp.Compare(b.Seq, a.Seq)
	})
	return out, unreviewedCount(pairs), nil
}

// signalRow lays a signal out for the kit's list: a barge-in opens its pair,
// anything else its conversation at the event that raised it.
func signalRow(sig triage.Signal) ui.ListRow {
	row := ui.ListRow{
		Tag: signalTags[sig.Kind], Title: sig.Utterance, Detail: sig.Detail,
		Who:    sig.Speaker + " · " + sig.Satellite,
		Figure: fmt.Sprintf("%s #%d", sig.ConversationID, sig.Seq),
		When:   sig.At.Local().Format("Jan 2 15:04"),
	}
	row.Href = conversationHref(sig.ConversationID) + fmt.Sprintf("#seq-%d", sig.Seq)
	if sig.PairID != "" {
		row.Href = reviewHref(sig.PairID)
	}
	return row
}

func (s *server) triage(w http.ResponseWriter, r *http.Request) {
	sigs, unreviewed, err := s.signals(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tab := r.URL.Query().Get("tab")
	if tab == "" {
		tab = "all"
	}

	tabs := ui.Tabs{ID: "queue-tabs", Label: "Signal"}
	list := ui.List{
		ID:      "queue",
		Columns: [6]string{"Signal", "Utterance · why", "", "Who · where", "Event", "When"},
		Empty:   &ui.EmptyState{Title: "Nothing in this pile.", Body: "Signals land here as the journal records them."},
	}
	for _, t := range triageTabs {
		n := 0
		for _, sig := range sigs {
			if t.kind == "" || sig.Kind == t.kind {
				n++
				if t.id == tab {
					list.Rows = append(list.Rows, signalRow(sig))
				}
			}
		}
		tabs.Items = append(tabs.Items, ui.TabItem{
			Label: t.label, Count: n, ShowCount: true, On: t.id == tab,
			Href: ui.Routes[ui.StepTriage] + "?tab=" + t.id,
		})
	}

	s.render(w, "page-triage", map[string]any{
		"Doc":    s.doc("Triage"),
		"Header": ui.NewAppHeader(ui.StepTriage, unreviewed, "chorus · journal"),
		"Head": ui.PageHead{
			Eyebrow: "02 · Triage", Title: "What's worth a listen",
			Subtitle: "Barge-ins, failures, repeated asks, slow answers and speaker flips, read from the journal.",
		},
		"Tabs": tabs,
		"List": list,
	})
}
