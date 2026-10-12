package vllm

import (
	"testing"
)

// The speak call's words are read out of arguments that have not finished
// arriving, holding back only what cannot yet be decoded whole, and only
// the text and mode arguments are read; anything else the model writes
// beside them is stepped over.
//
// verifies SPEC §4.1
func TestTheWordsAreReadOutOfArgumentsStillArriving(t *testing.T) {
	for _, c := range []struct {
		args      string
		text      string
		started   bool
		mode      string
		modeKnown bool
	}{
		{args: ``},
		{args: `{"te`},
		{args: `{"text"`},
		{args: `{"text": `},
		{args: `{"text": "`, started: true},
		{args: `{"text": "Turning off the kit`, text: "Turning off the kit", started: true},
		{args: `{"mode": "que`},
		{args: `{"mode": "queue", "text": "On`, text: "On", started: true, mode: "queue", modeKnown: true},
		{args: `{"text": "Done.", "mode": "pre`, text: "Done.", started: true},
		{args: `{"text": "Done.", "mode": "preempt"}`, text: "Done.", started: true, mode: "preempt", modeKnown: true},
		{args: `{"text": "Say \"hi\" \\ then\nstop`, text: "Say \"hi\" \\ then\nstop", started: true},
		{args: `{"text": "It's 18\u00`, text: "It's 18", started: true},
		{args: `{"text": "It's 18°C`, text: "It's 18°C", started: true},
		{args: `{"text": "eggs \ud83c`, text: "eggs ", started: true},
		{args: `{"text": "eggs \ud83c\`, text: "eggs ", started: true},
		{args: `{"text": "eggs 🍳`, text: "eggs 🍳", started: true},
		{args: `{"text": "lone \ud83c then"`, text: "lone � then", started: true},
		{args: "{\"text\": \"Sigur R\xc3", text: "Sigur R", started: true},
		{args: `{"volume": 40, "room": {"name": "kitchen", "tags": ["a", "}"]}, "text": "Forty`, text: "Forty", started: true},
		{args: `{"volume": 4`},
		{args: `{"text": 7}`},
	} {
		got := readSpeak(c.args)
		if got.Text != c.text || got.Started != c.started || got.Mode != c.mode || got.ModeKnown != c.modeKnown {
			t.Errorf("readSpeak(%q) = %+v, want text %q started %v mode %q known %v", c.args, got, c.text, c.started, c.mode, c.modeKnown)
		}
	}
}
