package session

import "strings"

// Candidate is a possible barge-in, as observed by the Listening child.
type Candidate struct {
	// PositionMS is playback offset, not wall clock: it is the quantity the
	// cut is reproducible against (SPEC §8).
	PositionMS int

	AudioRef string
	// AudioFrames is how much of AudioRef the gate judged: the blob is the
	// whole utterance's, kept once however many partials are judged. Zero
	// is all of it.
	AudioFrames int

	SpeakerID string
	Energy    float64

	// Echo is the Listening child's verdict that the audio is the device's
	// own voice coming back: it matches what the DAC was playing (ADR-0096).
	// EchoJudged says a comparison ran, with EchoScore and EchoLagMS what it
	// found; unjudged, the stage passes.
	Echo       bool
	EchoJudged bool
	EchoScore  float64
	EchoLagMS  int

	// Partial is the STT partial so far. One word is usually "uh", unless
	// it is a hot phrase such as "stop".
	Partial string
}

// Gate is the stacked barge-in detector of SPEC §4.3. Speaker identity is the
// high-value filter: the television and the wrong housemate both fail it.
type Gate struct {
	MinEnergy float64
	MinWords  int
	Household []string

	// SpeakerIDUnavailable skips the speaker stage: with no identifier every
	// voice is unidentified, so barge-in would be impossible (ADR-0031). Zero
	// keeps the stage; an unset gate stays strict.
	SpeakerIDUnavailable bool
}

// admit reports whether the candidate stops speech, which stage rejected it
// otherwise, and the hot phrase it was. There is deliberately no semantic
// check: it would spend latency where latency is felt (ADR-0004).
func (g Gate) admit(c Candidate, sessionSpeaker string) (stage string, hot HotWord, ok bool) {
	hot = Hot(c.Partial)
	if c.Energy < g.MinEnergy {
		return "vad", hot, false
	}
	// Before the speaker, and whatever the speaker: the device's own voice
	// is no reason to stop it, identified or not, and with no identifier at
	// all it is the only stage that can tell (ADR-0096).
	if c.Echo {
		return "echo", hot, false
	}
	// Unidentified while identification runs is the television, not a
	// missing sidecar.
	if !g.SpeakerIDUnavailable && !g.known(c.SpeakerID, sessionSpeaker) {
		return "speaker_id", hot, false
	}
	// A hot phrase skips only this stage: "stop" is one word (ADR-0064).
	if hot == "" && len(strings.Fields(c.Partial)) < g.MinWords {
		return "partial_length", hot, false
	}
	return "", hot, true
}

func (g Gate) known(speaker, sessionSpeaker string) bool {
	if speaker == "" {
		return false
	}
	if speaker == sessionSpeaker {
		return true
	}
	for _, m := range g.Household {
		if m == speaker {
			return true
		}
	}
	return false
}
