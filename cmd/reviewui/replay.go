package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/harvest"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/rerun"
	"github.com/teagan42/chorus/internal/reviewui/ui"
	sess "github.com/teagan42/chorus/internal/session"
)

// engineFactory builds the turn engine a re-run asks: the model, system
// prompt and tools as the reviewer edited them, and the versions that makes.
type engineFactory func(model, prompt string, tools map[string]registry.ToolSpec) (sess.Engine, journal.Versions, error)

// ollamaEngines builds Replay's engines on the configured endpoint, or none
// when it is not configured. The model must be configured too: it is the one
// chorusd runs, which says the endpoint is meant to be asked.
func ollamaEngines(baseURL, model string) engineFactory {
	if baseURL == "" || model == "" {
		return nil
	}
	return func(model, prompt string, tools map[string]registry.ToolSpec) (sess.Engine, journal.Versions, error) {
		e, err := ollama.New(ollama.Config{BaseURL: baseURL, Model: model, Prompt: prompt, Specs: tools})
		if err != nil {
			return nil, journal.Versions{}, err
		}
		return e, e.Versions(), nil
	}
}

// runTimeout bounds a whole re-run. A turn streams for as long as the model
// talks, and a conversation is a handful of turns.
const runTimeout = 3 * time.Minute

func replayHref(id string) string { return ui.Routes[ui.StepReplay] + "/" + id }

// replayable is what one conversation offers Replay: its turns as recorded,
// and how far the reducer got through its log.
type replayable struct {
	id     string
	start  time.Time
	turns  []rerun.Turn
	events int
	replay error
	unread int
	promos map[uint64]curation.Promotion
}

// readReplayable reads one conversation under the lock and lets it go: the
// model is asked afterwards, and it can think for a while.
func (s *server) readReplayable(ctx context.Context, id string) (replayable, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pairs, err := s.pairs(ctx, nil)
	if err != nil {
		return replayable{}, err
	}
	events, err := s.journal.Events(ctx, id)
	if err != nil {
		return replayable{}, err
	}
	rp := replayable{id: id, events: len(events), unread: unreviewedCount(pairs)}
	if len(events) == 0 {
		return rp, nil
	}
	rp.start = events[0].At
	if rp.turns, err = rerun.Turns(events); err != nil {
		return replayable{}, err
	}
	if rp.promos, err = s.decisions.Promotions(ctx, id); err != nil {
		return replayable{}, err
	}
	// The reducer is what keeps the log honest (SPEC §8): if it cannot read
	// the whole conversation back, nothing on this page can be trusted.
	_, rp.replay = journal.Replay(ctx, s.journal, id, journal.Overrides{})
	return rp, nil
}

func (s *server) replays(w http.ResponseWriter, r *http.Request) {
	var u unread
	s.mu.Lock()
	pairs, err := s.pairs(r.Context(), &u)
	var ids []string
	if err == nil {
		ids, err = s.journal.Conversations(r.Context())
	}
	type row struct {
		start time.Time
		row   ui.ListRow
	}
	var rows []row
	for _, id := range ids {
		if strings.HasPrefix(id, devicePrefix) || id == houseLog {
			continue
		}
		events, rerr := s.journal.Events(r.Context(), id)
		if rerr != nil {
			u.skip(id, rerr)
			continue
		}
		turns, rerr := rerun.Turns(events)
		if rerr != nil {
			u.skip(id, fmt.Errorf("replay: %w", rerr))
			continue
		}
		if len(turns) == 0 {
			continue
		}
		v := turns[0].Versions
		rows = append(rows, row{start: events[0].At, row: ui.ListRow{
			Href:   replayHref(id),
			Title:  turns[0].Text,
			Detail: fmt.Sprintf("%s · %s · %s · %s", id, v.Model, v.Prompt, v.ToolSchema),
			Who:    turns[0].Speaker,
			Figure: plural(len(turns), "turn"),
			When:   events[0].At.In(s.now().Location()).Format("2 Jan 15:04"),
		}})
	}
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	slices.SortFunc(rows, func(a, b row) int { return b.start.Compare(a.start) })
	list := ui.List{
		ID:      "replays",
		Columns: [6]string{"", "First ask · recorded under", "", "Who", "Turns", "When"},
		Empty:   &ui.EmptyState{Title: "Nothing to replay yet.", Body: "Conversations land here as the journal records them."},
	}
	for _, rw := range rows {
		list.Rows = append(list.Rows, rw.row)
	}
	s.render(w, "page-replays", map[string]any{
		"Doc":    s.doc("Replay"),
		"Header": ui.NewAppHeader(ui.StepReplay, unreviewedCount(pairs), "chorus · journal"),
		"Head": ui.PageHead{
			Eyebrow: "04 · Replay", Title: "Ask it again",
			Subtitle: "Re-run a conversation's turns under another prompt or model and see what it would have said and done.",
		},
		"List":   list,
		"Unread": u.alert(),
	})
}

// replayRow is one turn: what the journal recorded beside what the re-run
// produced, if it ran.
type replayRow struct {
	Anchor   string
	Seq      uint64
	Speaker  string
	Text     string
	Told     []string // what the turn was told it remembers, which a re-run is told too
	Recorded rerun.Take
	Replayed *rerun.Take
	Tag      ui.SigTag
	Promote  promoteCell
}

// promoteCell offers a changed take as the chosen side of a replay pair, or
// says one already was (SPEC §9.2).
type promoteCell struct {
	Seq      uint64
	Button   *ui.Button
	Promoted string // what the promoted take ran under
	Href     string // the pair in Curate
}

func replayPairID(conv string, seq uint64) string {
	return fmt.Sprintf("%s/%d/%s", conv, seq, harvest.SourceReplay)
}

func promotedCell(conv string, seq uint64, p curation.Promotion) promoteCell {
	return promoteCell{
		Seq: seq, Promoted: p.Versions.Model + " · " + p.Versions.Prompt + " · " + p.Versions.ToolSchema,
		Href: pairHref(replayPairID(conv, seq), "all"),
	}
}

// replayResult is the swappable half of the page: recorded turns before a
// run, the comparison after one.
type replayResult struct {
	Stats    *ui.MetricStrip
	Diff     *ui.CodeDiff
	ToolDiff *ui.CodeDiff
	Alert    *ui.Alert
	Rows     []replayRow
}

func (s *server) recordedRows(rp replayable) []replayRow {
	rows := make([]replayRow, 0, len(rp.turns))
	for _, t := range rp.turns {
		row := replayRow{
			Anchor: fmt.Sprintf("turn-%d", t.Seq), Seq: t.Seq, Speaker: t.Speaker, Text: t.Text,
			Told:     memoryLines(t.Speaker, t.Memories, t.Summaries, s.now().Location()),
			Recorded: t.Recorded, Tag: ui.SigTag{Text: "recorded", Tone: ui.ToneMuted},
			Promote: promoteCell{Seq: t.Seq},
		}
		if p, ok := rp.promos[t.Seq]; ok {
			row.Promote = promotedCell(rp.id, t.Seq, p)
		}
		rows = append(rows, row)
	}
	return rows
}

func (s *server) replayPage(w http.ResponseWriter, r *http.Request) {
	rp, err := s.readReplayable(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rp.events == 0 || len(rp.turns) == 0 {
		http.NotFound(w, r)
		return
	}
	recorded := rp.turns[len(rp.turns)-1].Versions

	check := ui.Metric{Label: "Journal replay", Value: fmt.Sprintf("replay() reads back all %d events", rp.events), Tone: ui.ToneMuted}
	if rp.replay != nil {
		check = ui.Metric{Label: "Journal replay", Value: "replay() fails", Note: rp.replay.Error(), Tone: ui.TonePeople}
	}
	promptNote, toolsNote, connect := "", "", (*ui.Alert)(nil)
	if s.engineFor == nil {
		connect = &ui.Alert{
			Title: "No model to ask.",
			Body:  "Set OLLAMA_URL and OLLAMA_MODEL for reviewui, the same endpoint chorusd uses, to re-run these turns.",
			Tone:  ui.ToneConv,
		}
	} else if _, v, err := s.engineFor(recorded.Model, ollama.DefaultPrompt, registry.Specs); err == nil {
		promptNote = "the default prompt (" + v.Prompt + ") matches the recorded prompt"
		if v.Prompt != recorded.Prompt {
			promptNote = fmt.Sprintf("recorded under %s; the default prompt is now %s, so this starts from the default", recorded.Prompt, v.Prompt)
		}
		toolsNote = "the registry's schema (" + v.ToolSchema + ") matches the recorded tool schema"
		if v.ToolSchema != recorded.ToolSchema {
			toolsNote = fmt.Sprintf("recorded under %s; the registry's schema is now %s, so this starts from the registry's", recorded.ToolSchema, v.ToolSchema)
		}
	}

	s.render(w, "page-replay", map[string]any{
		"Doc":    s.doc("Replay · " + rp.id),
		"Header": ui.NewAppHeader(ui.StepReplay, rp.unread, recorded.Model+" · "+recorded.Prompt+" · "+recorded.ToolSchema),
		"Head": ui.PageHead{
			Eyebrow: "04 · Replay", Trace: true, Title: rp.turns[0].Text, SubtitleMono: true,
			Subtitle: fmt.Sprintf("%s · %s · %s · %s", rp.id, rp.turns[0].Speaker, plural(len(rp.turns), "turn"), rp.start.In(s.now().Location()).Format("Mon 2 Jan 15:04")),
			Actions:  []ui.Button{{Label: "‹ Conversation", Href: conversationHref(rp.id)}},
		},
		"Recorded": ui.MetricStrip{Label: "Recorded", Items: []ui.Metric{
			{Label: "Model", Value: recorded.Model},
			{Label: "Prompt", Value: recorded.Prompt},
			{Label: "Tool schema", Value: recorded.ToolSchema},
			check,
		}},
		"Connect": connect,
		"Model":   ui.Field{Kind: ui.FieldInput, ID: "replay-model", Name: "model", Label: "Model", Value: recorded.Model},
		"Prompt": ui.Field{
			Kind: ui.FieldTextarea, ID: "replay-prompt", Name: "prompt", Label: "System prompt", Rows: 9,
			Value: ollama.DefaultPrompt,
		},
		"PromptNote": promptNote,
		"Tools": ui.Field{
			Kind: ui.FieldTextarea, ID: "replay-tools", Name: "tools", Label: "Tool schema", Rows: 12,
			Value: ollama.ToolSchema(registry.Specs),
		},
		"ToolsNote": toolsNote,
		"Run": ui.Button{
			Label: "Re-run every turn", Type: "submit", Primary: true, Disabled: s.engineFor == nil,
		},
		"Action": replayHref(rp.id),
		"Result": replayResult{Rows: s.recordedRows(rp)},
	})
}

func (s *server) replayRun(w http.ResponseWriter, r *http.Request) {
	if s.engineFor == nil {
		http.Error(w, "no model configured: set OLLAMA_URL and OLLAMA_MODEL", http.StatusServiceUnavailable)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rp, err := s.readReplayable(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(rp.turns) == 0 {
		http.NotFound(w, r)
		return
	}
	// A refusal is still swapped in: htmx drops a 4xx body, and a button
	// that silently does nothing is the worst answer.
	model, prompt := strings.TrimSpace(r.PostForm.Get("model")), r.PostForm.Get("prompt")
	schema := ollama.ToolSchema(registry.Specs)
	if r.PostForm.Has("tools") {
		schema = r.PostForm.Get("tools")
	}
	if model == "" || strings.TrimSpace(prompt) == "" || strings.TrimSpace(schema) == "" {
		s.render(w, "replay-result", replayResult{
			Alert: &ui.Alert{Title: "Nothing to run.", Body: "A re-run needs a model, a system prompt and a tool schema.", Tone: ui.ToneConv},
			Rows:  s.recordedRows(rp),
		})
		return
	}
	tools, err := ollama.ParseToolSchema(schema)
	if err != nil {
		s.render(w, "replay-result", replayResult{
			Alert: &ui.Alert{Title: "The tool schema does not read.", Body: err.Error(), Note: "nothing was asked", Tone: ui.ToneConv},
			Rows:  s.recordedRows(rp),
		})
		return
	}
	eng, v, err := s.engineFor(model, prompt, tools)
	if err != nil {
		s.render(w, "replay-result", replayResult{
			Alert: &ui.Alert{Title: "Cannot build the engine.", Body: err.Error(), Tone: ui.ToneConv},
			Rows:  s.recordedRows(rp),
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), runTimeout)
	defer cancel()
	res := replayResult{Rows: s.recordedRows(rp)}
	speech, calls, ran := 0, 0, 0
	promote := replayHref(rp.id) + "/turns/"
	for i, t := range rp.turns {
		take, err := rerun.Run(ctx, eng, rp.id, t)
		if err != nil {
			res.Alert = &ui.Alert{
				Title: fmt.Sprintf("Re-run stopped at #%d.", t.Seq),
				Body:  err.Error(),
				Note:  "turns before it ran; the rest show what was recorded",
				Tone:  ui.ToneConv,
			}
			break
		}
		ran++
		c := rerun.Compare(t.Recorded, take)
		row := &res.Rows[i]
		row.Replayed = &take
		switch {
		case c.Speech && c.Calls:
			row.Tag = ui.SigTag{Text: "speech and calls changed", Tone: ui.ToneVoice}
		case c.Speech:
			row.Tag = ui.SigTag{Text: "speech changed", Tone: ui.ToneVoice}
		case c.Calls:
			row.Tag = ui.SigTag{Text: "calls changed", Tone: ui.ToneHome}
		default:
			row.Tag = ui.SigTag{Text: "same", Tone: ui.ToneMuted}
		}
		// A take that only calls is a chosen side; one that does nothing is not.
		if (c.Speech || c.Calls) && (strings.TrimSpace(take.Said()) != "" || len(take.Calls) > 0) {
			row.Promote.Button = promoteButton(promote, t.Seq, take)
		}
		if c.Speech {
			speech++
		}
		if c.Calls {
			calls++
		}
	}
	res.Stats = &ui.MetricStrip{Label: "Outcome", Items: []ui.Metric{
		{Label: "Turns re-run", Value: fmt.Sprintf("%d of %d", ran, len(rp.turns))},
		{Label: "Speech changed", Value: fmt.Sprintf("%d of %d", speech, ran), Tone: when(speech > 0, ui.ToneVoice)},
		{Label: "Tool calls changed", Value: fmt.Sprintf("%d of %d", calls, ran), Tone: when(calls > 0, ui.ToneHome)},
		{Label: "Ran under", Value: v.Model + " · " + v.Prompt + " · " + v.ToolSchema, Note: "tools are compared, never executed"},
	}}
	if d := lineDiff(ollama.DefaultPrompt, prompt); d != nil {
		res.Diff = &ui.CodeDiff{Label: "Prompt diff", Lines: d}
	}
	// Both sides written the same way, so a reindented edit is not a change.
	if d := lineDiff(ollama.ToolSchema(registry.Specs), ollama.ToolSchema(tools)); d != nil {
		res.ToolDiff = &ui.CodeDiff{Label: "Tool schema diff", Lines: hunks(d, 3)}
	}
	s.render(w, "replay-result", res)
}

// promoteButton posts the take back with the model, prompt and tools that made it,
// so the stored pair says where its chosen side came from.
func promoteButton(base string, seq uint64, take rerun.Take) *ui.Button {
	calls := make([]curation.Call, 0, len(take.Calls))
	for _, c := range take.Calls {
		calls = append(calls, curation.Call{Tool: c.Tool, Args: c.Args})
	}
	cj, _ := json.Marshal(calls) // two strings a call cannot fail to encode
	vals, _ := json.Marshal(map[string]string{"speech": take.Said(), "calls": string(cj)})
	cell := fmt.Sprintf("#promote-%d", seq)
	return &ui.Button{Label: "Promote", Hx: ui.Hx{
		Post: base + strconv.FormatUint(seq, 10) + "/promote", Target: cell, Swap: "outerHTML",
		Include: "#replay-model, #replay-prompt, #replay-tools", Vals: string(vals),
	}}
}

// promote handles POST /replays/{id}/turns/{seq}/promote: the re-run's take
// becomes the chosen side of a pair whose rejected side is what the turn
// recorded, accepted, since the reviewer just judged it (SPEC §9.2).
func (s *server) promote(w http.ResponseWriter, r *http.Request) {
	if s.engineFor == nil {
		http.Error(w, "no model configured: set OLLAMA_URL and OLLAMA_MODEL", http.StatusServiceUnavailable)
		return
	}
	seq, err := strconv.ParseUint(r.PathValue("seq"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f := r.PostForm
	speech, model, prompt := strings.TrimSpace(f.Get("speech")), strings.TrimSpace(f.Get("model")), f.Get("prompt")
	schema := ollama.ToolSchema(registry.Specs)
	if f.Has("tools") {
		schema = f.Get("tools")
	}
	var calls []curation.Call
	if raw := f.Get("calls"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &calls); err != nil {
			http.Error(w, "calls: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if (speech == "" && len(calls) == 0) || model == "" || strings.TrimSpace(prompt) == "" {
		http.Error(w, "a promotion needs what the re-run said or called, its model and its prompt", http.StatusBadRequest)
		return
	}
	rp, err := s.readReplayable(r.Context(), r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !slices.ContainsFunc(rp.turns, func(t rerun.Turn) bool { return t.Seq == seq }) {
		http.NotFound(w, r)
		return
	}
	tools, err := ollama.ParseToolSchema(schema)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// The versions are the server's to say, from the model, prompt and tools sent.
	_, v, err := s.engineFor(model, prompt, tools)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	p := curation.Promotion{
		ConversationID: rp.id, Seq: seq, Speech: speech, Calls: calls,
		Versions: v, SystemPrompt: prompt, ToolSchema: ollama.ToolSchema(tools), PromotedAt: s.now(),
	}
	s.mu.Lock()
	err = s.decisions.PutPromotion(r.Context(), p)
	if err == nil {
		err = s.decisions.Put(r.Context(), curation.Decision{
			PairID: replayPairID(rp.id, seq), ConversationID: rp.id,
			Status: curation.StatusAccepted, Chosen: speech, Prev: "unreviewed", DecidedAt: s.now(),
		})
	}
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.render(w, "replay-promote", promotedCell(rp.id, seq, p))
}

// hunks keeps each change and n lines either side, marking what it leaves
// out: the registry's schema is hundreds of lines, and an edit is a few.
func hunks(d []ui.DiffLine, n int) []ui.DiffLine {
	near := make([]bool, len(d))
	for i, l := range d {
		if l.Kind != " " {
			for j := max(0, i-n); j <= min(len(d)-1, i+n); j++ {
				near[j] = true
			}
		}
	}
	var out []ui.DiffLine
	for i, l := range d {
		switch {
		case near[i]:
			out = append(out, l)
		case i == 0 || near[i-1]:
			out = append(out, ui.DiffLine{Kind: " ", Text: "…"})
		}
	}
	return out
}

func when(ok bool, t ui.Tone) ui.Tone {
	if ok {
		return t
	}
	return ""
}

// lineDiff is the edit from a to b, line by line, or nil when they match.
// Prompts are a few dozen lines; the quadratic table is nothing.
func lineDiff(a, b string) []ui.DiffLine {
	if a == b {
		return nil
	}
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	var out []ui.DiffLine
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			out = append(out, ui.DiffLine{Kind: " ", Text: x[i]})
			i, j = i+1, j+1
		case j < len(y) && (i == len(x) || lcs[i][j+1] >= lcs[i+1][j]):
			out = append(out, ui.DiffLine{Kind: "+", Text: y[j]})
			j++
		default:
			out = append(out, ui.DiffLine{Kind: "-", Text: x[i]})
			i++
		}
	}
	return out
}
