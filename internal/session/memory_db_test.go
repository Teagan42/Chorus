//go:build db

package session_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/memory"
	"github.com/teagan42/chorus/internal/session"
)

// Teagan tells the kitchen about the oat milk with the journal and memory in
// Postgres, as chorusd runs them. After a restart the next conversation is
// told, and a replay of that conversation from the database recalls the oat
// milk even once Teagan has asked to forget it.
//
// verifies SPEC §5, §8
func TestWhatTeaganAskedToBeRememberedSurvivesARestartInPostgres(t *testing.T) {
	ctx := context.Background()
	pool := openPg(t)
	if err := memory.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate memory: %v", err)
	}
	// The whole table: what another package's suite shared would be recalled
	// to Teagan too. `task test:db` runs packages one at a time.
	if _, err := pool.Exec(ctx, `TRUNCATE memories, conversation_summaries`); err != nil {
		t.Fatalf("clear memories: %v", err)
	}
	pg := journal.NewPgStore(pool)
	mems := memory.NewPgStore(pool)

	daemon := func(m *model) *rig {
		return newRigWith(t, nil, nil, nil, func(cfg *session.Config) {
			cfg.Engine = m
			cfg.Store = pg
			cfg.Journal = journal.New(pg, cfg.Clock, versions())
			cfg.Tools = memory.Tools(mems, cfg.Clock)
			cfg.Memories = memory.Recaller(mems, memory.RecallConfig{})
		})
	}

	yesterday := daemon(&model{answer: func(in session.Input) []session.Action {
		if in.Dialogue[len(in.Dialogue)-1].Kind == journal.EntryHeard {
			return []session.Action{session.ToolCall{ID: "call_r1", Tool: "remember", Args: `{"fact":"Takes oat milk in coffee."}`}, done}
		}
		return []session.Action{done}
	}})
	s := yesterday.open(t, "teagan")
	wait(t, heard(s, "remember that I take oat milk in my coffee"))
	st, err := journal.Replay(ctx, pg, s.ConversationID(), journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if c := callByID(t, st, "call_r1"); c.Outcome != "ok" {
		t.Fatalf("remember = %+v", c)
	}

	var told []journal.Memory
	today := daemon(&model{answer: func(in session.Input) []session.Action {
		told = in.Memories
		return []session.Action{done}
	}})
	s = today.open(t, "teagan")
	wait(t, heard(s, "how do I take my coffee"))
	if len(told) != 1 || told[0].Fact != "Takes oat milk in coffee." || told[0].Person != "teagan" {
		t.Fatalf("this morning's ask was told %+v, want the oat milk", told)
	}

	if gone, err := mems.Forget(ctx, "teagan", told[0].ID); err != nil || !gone {
		t.Fatalf("forget: gone=%v err=%v", gone, err)
	}
	st, err = journal.Replay(ctx, pg, s.ConversationID(), journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(st.Recalled) != 1 || st.Recalled[0] != told[0] {
		t.Errorf("replay recalled %+v, want what the turn was told: %+v", st.Recalled, told)
	}
}

// Teagan's conversation about the garage door ends with the journal and
// memory in Postgres. The summary is kept there, and the next conversation,
// after a restart, is told it and the time, as its own log records.
//
// verifies SPEC §5, §8
func TestWhatTeaganAskedYesterdayIsToldFromPostgres(t *testing.T) {
	ctx := context.Background()
	pool := openPg(t)
	if err := memory.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate memory: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE memories, conversation_summaries`); err != nil {
		t.Fatalf("clear memories: %v", err)
	}
	pg := journal.NewPgStore(pool)
	mems := memory.NewPgStore(pool)
	const garage = "teagan asked whether the garage door was closed; the assistant said it was."

	var summarizing sync.WaitGroup
	daemon := func(m *model) *rig {
		return newRigWith(t, nil, nil, nil, func(cfg *session.Config) {
			cfg.Engine = m
			cfg.Store = pg
			cfg.Journal = journal.New(pg, cfg.Clock, versions())
			cfg.Memories = memory.Recaller(mems, memory.RecallConfig{})
			cfg.Summarizer = &summarizer{text: garage}
			cfg.Summarizing = &summarizing
		})
	}

	yesterday := daemon(&model{answer: func(session.Input) []session.Action { return []session.Action{done} }})
	s := yesterday.open(t, "teagan")
	wait(t, heardFrom(s, "teagan", "is the garage door closed"))
	if err := s.Close(ctx, "model_ended"); err != nil {
		t.Fatalf("close: %v", err)
	}
	summarizing.Wait()
	first := s.ConversationID()

	var told session.Input
	today := daemon(&model{answer: func(in session.Input) []session.Action {
		told = in
		return []session.Action{done}
	}})
	today.clock.advance(20 * time.Hour)
	s = today.open(t, "teagan")
	wait(t, heardFrom(s, "teagan", "what did I ask you yesterday"))
	if len(told.Summaries) != 1 || told.Summaries[0].ConversationID != first || told.Summaries[0].Text != garage ||
		!told.Summaries[0].At.Equal(epoch) || !told.Now.Equal(epoch.Add(20*time.Hour)) {
		t.Fatalf("this morning's ask was told %+v at %v", told.Summaries, told.Now)
	}
	st, err := journal.Replay(ctx, pg, s.ConversationID(), journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(st.RecalledSummaries) != 1 || st.RecalledSummaries[0].Text != garage || !st.HeardAt.Equal(told.Now) {
		t.Errorf("replay recalled %+v at %v, want what the turn was told", st.RecalledSummaries, st.HeardAt)
	}
}

// garageWords is an embedding model that knows one topic: how much a text
// is about getting into the garage.
type garageWords struct{}

func (garageWords) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		t = strings.ToLower(t)
		out[i] = []float32{float32(strings.Count(t, "garage") + strings.Count(t, "code")), 0.05}
	}
	return out, nil
}

func (garageWords) EmbedModel() string { return "nomic-embed-text" }

// Teagan told the house the garage code in August and two dozen things
// since, all in Postgres. Asked for the code, the turn is told it, and a
// replay from the database says which model chose it.
//
// verifies SPEC §5, §8
func TestTheGarageCodeIsChosenFromPostgres(t *testing.T) {
	ctx := context.Background()
	pool := openPg(t)
	if err := memory.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate memory: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE memories, conversation_summaries`); err != nil {
		t.Fatalf("clear memories: %v", err)
	}
	pg := journal.NewPgStore(pool)
	mems := memory.NewPgStore(pool)
	august := epoch.Add(-60 * 24 * time.Hour)
	code := memory.Memory{ID: "m_9a7e4512", Person: "teagan", Fact: "The garage door code is 4512.", At: august}
	if err := mems.Remember(ctx, code); err != nil {
		t.Fatalf("remember: %v", err)
	}
	for i := range 24 {
		if err := mems.Remember(ctx, memory.Memory{
			ID: fmt.Sprintf("m_teagan%02d", i), Person: "teagan", At: august.Add(time.Duration(i+1) * 24 * time.Hour),
			Fact: fmt.Sprintf("Watered the porch plants on day %d of the month.", i+1),
		}); err != nil {
			t.Fatalf("remember: %v", err)
		}
	}

	var told session.Input
	r := newRigWith(t, nil, nil, nil, func(cfg *session.Config) {
		cfg.Engine = &model{answer: func(in session.Input) []session.Action {
			told = in
			return []session.Action{done}
		}}
		cfg.Store = pg
		cfg.Journal = journal.New(pg, cfg.Clock, versions())
		cfg.Memories = memory.Recaller(mems, memory.RecallConfig{Embedder: garageWords{}})
	})
	s := r.open(t, "teagan")
	wait(t, heardFrom(s, "teagan", "what's the code for the garage"))
	if len(told.Memories) != memory.RecallLimit || !slices.Contains(told.Memories, code.Recalled()) {
		t.Fatalf("the turn was told %d memories without the code: %+v", len(told.Memories), told.Memories)
	}
	st, err := journal.Replay(ctx, pg, s.ConversationID(), journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !slices.Equal(st.Recalled, told.Memories) || st.RecalledRankedBy != "nomic-embed-text" {
		t.Errorf("replay recalled %d ranked by %q, want what the turn was told, ranked", len(st.Recalled), st.RecalledRankedBy)
	}
}
