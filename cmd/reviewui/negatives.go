package main

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/reviewui/ui"
)

// negative is one rejected wake with the reviewer's word on it, if any.
type negative struct {
	harvest.Negative
	status curation.WakeStatus
}

// ships is the wake corpus's one rule: auto-labelled unless discarded
// (SPEC §9.3), but a wake only the voice check failed waits for a yes.
func (n negative) ships() bool {
	switch n.status {
	case curation.WakeDiscarded:
		return false
	case curation.WakeConfirmed:
		return true
	}
	return !n.Held()
}

// negatives reads every satellite's rejected wakes, oldest first, with the
// verdicts on them.
func (s *server) negatives(ctx context.Context, u *unread) ([]negative, error) {
	convs, err := s.journal.Conversations(ctx)
	if err != nil {
		return nil, err
	}
	return s.negativesOf(ctx, convs, u)
}

// negativesOf is negatives over a listing the caller already holds.
func (s *server) negativesOf(ctx context.Context, convs []string, u *unread) ([]negative, error) {
	var out []negative
	for _, conv := range convs {
		if !strings.HasPrefix(conv, devicePrefix) {
			continue
		}
		d, err := s.logs.of(ctx, s.journal, conv)
		if err == nil {
			err = d.negErr
		}
		if err != nil {
			u.skip(conv, err)
			continue
		}
		vs, err := s.decisions.WakeVerdicts(ctx, conv)
		if err != nil {
			return nil, err
		}
		for _, n := range d.negatives {
			out = append(out, negative{Negative: n, status: vs[n.Seq].Status})
		}
	}
	slices.SortFunc(out, func(a, b negative) int {
		return cmp.Or(a.At.Compare(b.At), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// wakeCorpus is what ships, each row saying whether a reviewer confirmed it.
func wakeCorpus(ns []negative) []harvest.Negative {
	var out []harvest.Negative
	for _, n := range ns {
		if n.ships() {
			h := n.Negative
			h.Confirmed = n.status == curation.WakeConfirmed
			out = append(out, h)
		}
	}
	return out
}

// exportWake serves the wake corpus, its own file beside the DPO dataset.
func (s *server) exportWake(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	ns, err := s.negatives(r.Context(), nil)
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/jsonl")
	w.Header().Set("Content-Disposition", `attachment; filename="chorus-wake-negatives.jsonl"`)
	if err := harvest.ExportNegatives(w, wakeCorpus(ns)); err != nil {
		log.Printf("reviewui: wake export aborted: %v", err)
		panic(http.ErrAbortHandler)
	}
}

// wakeCounts are the wake corpus's piles, as the Export page explains them.
type wakeCounts struct{ Ships, Confirmed, Held, Discarded int }

func countWakes(ns []negative) wakeCounts {
	var c wakeCounts
	for _, n := range ns {
		switch {
		case n.status == curation.WakeDiscarded:
			c.Discarded++
		case !n.ships():
			c.Held++
		case n.status == curation.WakeConfirmed:
			c.Ships++
			c.Confirmed++
		default:
			c.Ships++
		}
	}
	return c
}

// wakeView is the verdict under a rejected wake on its satellite's log,
// swapped whole on every change.
type wakeView struct {
	ID               string
	Seq              uint64
	Note             string
	Confirm, Discard ui.Button
}

func wakeVerdictView(conv string, n negative) wakeView {
	v := wakeView{ID: fmt.Sprintf("wake-%d", n.Seq), Seq: n.Seq}
	button := func(label string, st curation.WakeStatus) ui.Button {
		return ui.Button{
			Label: label, Toggle: true, Pressed: n.status == st,
			Hx: ui.Hx{
				Post:   fmt.Sprintf("%s/wakes/%d/%s", conversationHref(conv), n.Seq, st),
				Target: "#" + v.ID, Swap: "outerHTML",
			},
		}
	}
	v.Confirm, v.Discard = button("Confirm negative", curation.WakeConfirmed), button("Discard", curation.WakeDiscarded)
	switch {
	case n.status == curation.WakeDiscarded:
		v.Note = "Kept out of the wake corpus."
	case n.status == curation.WakeConfirmed:
		v.Note = "Confirmed: ships with the wake corpus."
	case n.Held():
		v.Note = "Held: only the voice check failed, so this may be the wake word from a guest. Confirm it to ship."
	default:
		v.Note = "Hard negative: ships with the wake corpus unless you discard it."
	}
	return v
}

// wakeVerdict handles POST /conversations/{id}/wakes/{seq}/{status}: a second
// press of the same verdict takes it off.
func (s *server) wakeVerdict(w http.ResponseWriter, r *http.Request) {
	conv := r.PathValue("id")
	seq, err := strconv.ParseUint(r.PathValue("seq"), 10, 64)
	st := curation.WakeStatus(r.PathValue("status"))
	if err != nil || !strings.HasPrefix(conv, devicePrefix) || (st != curation.WakeConfirmed && st != curation.WakeDiscarded) {
		http.NotFound(w, r)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := r.Context()
	ns, err := harvest.Negatives(ctx, s.journal, conv)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	i := slices.IndexFunc(ns, func(n harvest.Negative) bool { return n.Seq == seq })
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	vs, err := s.decisions.WakeVerdicts(ctx, conv)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if vs[seq].Status == st {
		st = ""
	}
	if err := s.decisions.PutWakeVerdict(ctx, curation.WakeVerdict{
		ConversationID: conv, Seq: seq, Status: st, JudgedAt: s.now(),
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.render(w, "wake-verdict", wakeVerdictView(conv, negative{Negative: ns[i], status: st}))
}
