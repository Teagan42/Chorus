package ui

// Tone is the semantic colour of something. Colour is meaning, never
// affordance: each brand colour owns exactly one idea.
type Tone string

const (
	ToneNone   Tone = ""
	ToneVoice  Tone = "voice"  // assistant speech, latency, wake-word audio
	ToneConv   Tone = "conv"   // session lifecycle, wake/end, room migration, repeats, replays
	TonePeople Tone = "people" // speaker identity and the barge-in cut
	ToneHome   Tone = "home"   // tool calls into the house
	ToneHuman  Tone = "human"  // reviewer-authored things, neutral emphasis
	ToneMuted  Tone = "muted"  // settled, rejected, system
)

// Class is the CSS class that sets --tone, --tone-text, --tone-line, --tone-on.
func (t Tone) Class() string {
	if t == ToneNone {
		return ""
	}
	return "tone-" + string(t)
}
