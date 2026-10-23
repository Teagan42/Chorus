package main

import (
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/listen"
)

// CHORUS_SMARTTURN_SURE and CHORUS_SMARTTURN_DOUBT move the bands Smart
// Turn's probability is read in. A band outside 0 to 1 never or always
// fires, and doubt above sure leaves a probability no band at all, so each is
// refused at startup rather than at the first pause. Equal is the single cut
// chorusd had before the bands (ADR-0094).
//
// verifies SPEC §4.5
func TestSmartTurnsBandsAreConfigurable(t *testing.T) {
	for _, tc := range []struct {
		sure, doubt       string
		wantSure, wantDbt float64
		refused           bool
	}{
		{sure: "", doubt: "", wantSure: 0, wantDbt: 0},
		{sure: "0.7", doubt: "", wantSure: 0.7, wantDbt: 0},
		{sure: "", doubt: "0.5", wantSure: 0, wantDbt: 0.5},
		{sure: "0.5", doubt: "0.5", wantSure: 0.5, wantDbt: 0.5},
		{sure: "0.9", doubt: "0.2", wantSure: 0.9, wantDbt: 0.2},
		{sure: "0.25", doubt: "", refused: true},
		{sure: "", doubt: "0.85", refused: true},
		{sure: "0.4", doubt: "0.6", refused: true},
		{sure: "1", refused: true},
		{doubt: "0", refused: true},
		{sure: "-0.2", refused: true},
		{sure: "sure", refused: true},
	} {
		sure, doubt, err := (Config{SmartTurnSure: tc.sure, SmartTurnDoubt: tc.doubt}).turnBands()
		switch {
		case tc.refused && err == nil:
			t.Errorf("sure %q, doubt %q were accepted", tc.sure, tc.doubt)
		case !tc.refused && err != nil:
			t.Errorf("sure %q, doubt %q were refused: %v", tc.sure, tc.doubt, err)
		case !tc.refused && (sure != tc.wantSure || doubt != tc.wantDbt):
			t.Errorf("sure %q, doubt %q parsed to %v, %v", tc.sure, tc.doubt, sure, doubt)
		}
	}
}

// A refused band stops chorusd at startup with the variables named, not at
// the kitchen satellite's first pause.
//
// verifies SPEC §4.5
func TestARefusedBandIsAStartupError(t *testing.T) {
	env := complete()
	env[smartTurnSureEnv], env[smartTurnDoubtEnv] = "0.4", "0.6"
	err := configFromEnv(lookup(env)).validate()
	if err == nil {
		t.Fatal("doubt above sure was accepted")
	}
	for _, name := range []string{smartTurnSureEnv, smartTurnDoubtEnv} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the error does not name %s: %v", name, err)
		}
	}
}

// Each link's endpointer reads the household's bands, and the words are only
// waited for above its doubt.
//
// verifies SPEC §4.5
func TestTheEndpointerTakesTheHouseholdsBands(t *testing.T) {
	s, ok := (providers{judge: &turnJudge{}, sure: 0.7, doubt: 0.4}).endpointer(0).(*listen.Semantic)
	if !ok {
		t.Fatal("a configured judge did not produce a semantic endpointer")
	}
	if s.Sure != 0.7 || s.Doubt != 0.4 {
		t.Errorf("bands are %v and %v, want the household's 0.7 and 0.4", s.Sure, s.Doubt)
	}
	if d, ok := s.Judge.(listen.Dangling); !ok || d.Doubt != 0.4 {
		t.Errorf("judge is %#v, want the words read above the household's doubt", s.Judge)
	}

	s = (providers{judge: &turnJudge{}}).endpointer(0).(*listen.Semantic)
	if s.Sure != listen.DefaultSure || s.Doubt != listen.DefaultDoubt {
		t.Errorf("bands are %v and %v with none set, want the defaults", s.Sure, s.Doubt)
	}
}
