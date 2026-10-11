//go:build db

package session_test

import (
	"context"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/session"
)

// Teagan's front door with the journal in Postgres, as chorusd runs it: the
// nonce is redeemed against the log the database holds, once, and a replay
// from that log says who said yes to what.
//
// verifies SPEC §6, §8
func TestTheFrontDoorIsConfirmedFromPostgres(t *testing.T) {
	pg := journal.NewPgStore(openPg(t))
	lock := &lockTool{}
	m := &model{answer: frontDoorModel}
	r := newRigWith(t, nil, map[string]session.Tool{"ha_call_service": lock}, nil, func(cfg *session.Config) {
		cfg.Engine = m
		cfg.Store = pg
		cfg.Journal = journal.New(pg, cfg.Clock, versions())
	})

	s := r.open(t, "teagan")
	wait(t, heard(s, "unlock the front door"))
	if got := lock.calls(); len(got) != 0 {
		t.Fatalf("the lock was sent %q before Teagan said yes", got)
	}
	wait(t, heard(s, "yes please"))
	if got := lock.calls(); len(got) != 1 {
		t.Fatalf("the lock was sent %q, want the front door once", got)
	}

	st, err := journal.Replay(context.Background(), pg, s.ConversationID(), journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(st.Confirmations) != 1 {
		t.Fatalf("confirmations = %+v, want the one front door", st.Confirmations)
	}
	c := st.Confirmations[0]
	if c.CallID != "call_c1" || c.Answer != "yes please" || c.RedeemedBy != "call_c2" {
		t.Errorf("confirmation = %+v, want call_c1 held, answered %q, redeemed by call_c2", c, "yes please")
	}
	if got := st.Redeemable(c.Nonce, "ha_call_service", c.Args); got != journal.RefusedUsed {
		t.Errorf("a second redemption = %q, want %q", got, journal.RefusedUsed)
	}
}

// Alice's yes to Teagan's question, with the journal in Postgres: the log
// the database holds says who asked, refuses Alice's yes as someone else's,
// and redeems the fresh nonce on Teagan's.
//
// verifies SPEC §5, §6, §8
func TestAlicesYesIsRefusedFromPostgres(t *testing.T) {
	pg := journal.NewPgStore(openPg(t))
	lock := &lockTool{}
	m := &model{answer: askerModel()}
	r := newRigWith(t, nil, map[string]session.Tool{"ha_call_service": lock}, nil, func(cfg *session.Config) {
		cfg.Engine = m
		cfg.Store = pg
		cfg.Journal = journal.New(pg, cfg.Clock, versions())
	})

	s := r.open(t, "teagan")
	wait(t, voiced(s, "unlock the front door", "teagan", "identified"))
	wait(t, voiced(s, "yes please", "alice", "identified"))
	if got := lock.calls(); len(got) != 0 {
		t.Fatalf("the lock was sent %q on Alice's yes to Teagan's question", got)
	}
	wait(t, voiced(s, "yes, go ahead", "teagan", "identified"))
	if got := lock.calls(); len(got) != 1 {
		t.Fatalf("the lock was sent %q, want the front door once on Teagan's yes", got)
	}

	st, err := journal.Replay(context.Background(), pg, s.ConversationID(), journal.Overrides{})
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(st.Confirmations) != 2 {
		t.Fatalf("confirmations = %+v, want the front door held twice", st.Confirmations)
	}
	first, fresh := st.Confirmations[0], st.Confirmations[1]
	if first.AskedBy != "teagan" || first.AnsweredBy != "alice" || first.RefusedBy != "call_c2" {
		t.Errorf("first = %+v, want Teagan's question answered by Alice and refused on call_c2", first)
	}
	if fresh.AskedBy != "teagan" || fresh.AnsweredBy != "teagan" || fresh.RedeemedBy != "call_c3" {
		t.Errorf("fresh = %+v, want Teagan's question answered by Teagan and redeemed by call_c3", fresh)
	}
}
