package journal

import "github.com/teagan42/chorus/internal/registry"

// Why a nonce was not accepted, as confirmation_requested records it.
const (
	// RefusedUnknown: no call in this conversation was handed the nonce.
	RefusedUnknown = "unknown"
	// RefusedUsed: the nonce has had its one try, whether it let a call run
	// or was refused.
	RefusedUsed = "used"
	// RefusedArgsChanged: the nonce was for another call. A yes to the front
	// door is not a yes to the back door.
	RefusedArgsChanged = "args_changed"
	// RefusedNotAnswered: the person has said nothing since the nonce was
	// handed out, so nobody can have said yes.
	RefusedNotAnswered = "not_answered"
	// RefusedExpired: the person has said more than one thing since. The
	// answer was the first of them, and its turn is over.
	RefusedExpired = "expired"
	// RefusedWrongPerson: someone other than the person who asked answered.
	// The question was theirs, so the yes must be too.
	RefusedWrongPerson = "wrong_person"
	// RefusedGuest: the answer came from a voice that matched nobody in the
	// household, and a guest's yes runs nothing.
	RefusedGuest = "guest"
)

// Confirmation is a call that was held for the person's yes, and what became
// of the nonce it was handed (SPEC §6).
type Confirmation struct {
	Nonce  string
	CallID string
	Tool   string

	// Args are the held call's arguments without a nonce, as
	// registry.Split encodes them: what a redeeming call must match.
	Args string

	// AskedBy is who the log attributes the turn that made the held call
	// to: whose yes the nonce waits for, when it names anyone (ADR-0063).
	// A fresh nonce handed out on a refusal keeps the first asker.
	AskedBy string

	// Heard counts what the person has said since the nonce was handed out.
	Heard int

	// Answer is the first thing said after the question, AnsweredBy who the
	// log attributes it to, and AnswerMatch how their voice matched: the
	// "yes" a redeemed nonce stands on, for whoever audits it.
	Answer      string
	AnsweredBy  string
	AnswerMatch string

	// RedeemedBy is the call that ran on this nonce, and RefusedBy the call
	// that presented it and was refused. A nonce gets one try: a refused one
	// would otherwise stay open and could ride the answer to a different
	// question. Both are empty while it is open.
	RedeemedBy string
	RefusedBy  string
}

// open reports whether the nonce has not had its try yet.
func (c Confirmation) open() bool { return c.RedeemedBy == "" && c.RefusedBy == "" }

// Redeemable decides whether a call may run on nonce. Args are the call's
// arguments without the nonce, encoded by registry.Split. It returns
// the refusal, or "" when the call may run.
//
// Whether the answer was a yes is the model's to judge: it wrote the
// question. What the log guarantees is that the person said something in
// between, so a model cannot confirm its own call (ADR-0038), and that the
// yes is the asker's (ADR-0063): a voice judged to be nobody's cannot
// confirm anything, and when the asker was identified, only their answer
// counts. A household nothing identifies keeps the next thing said as the
// answer, since SPEC §5 leaves a voice nothing judged with the current
// speaker, and nobody_enrolled can tell no one apart.
func (s State) Redeemable(nonce, tool, args string) string {
	i := s.confirmation(nonce)
	if i < 0 {
		return RefusedUnknown
	}
	c := s.Confirmations[i]
	switch {
	case !c.open():
		return RefusedUsed
	case c.Tool != tool || c.Args != args:
		return RefusedArgsChanged
	case c.Heard == 0:
		return RefusedNotAnswered
	case c.Heard > 1:
		return RefusedExpired
	case Unmatched(c.AnswerMatch):
		return RefusedGuest
	case c.AskedBy != "" && c.AnsweredBy != c.AskedBy:
		return RefusedWrongPerson
	}
	return ""
}

func (s State) confirmation(nonce string) int {
	for i, c := range s.Confirmations {
		if c.Nonce == nonce {
			return i
		}
	}
	return -1
}

// requested opens a confirmation for a held call, and spends the nonce the
// call presented, if it presented one this conversation handed out.
func (s State) requested(callID, nonce, presented string) ([]Confirmation, bool) {
	i := indexOfCall(s.Calls, callID)
	if i < 0 {
		return nil, false
	}
	call := s.Calls[i]
	args := call.Args
	if o, err := registry.Split(call.Args); err == nil {
		args = o.Rest
	}
	// Otherwise only a hand-written log: the session holds no call it cannot
	// read. Raw arguments still compare equal to themselves.
	out := make([]Confirmation, len(s.Confirmations), len(s.Confirmations)+1)
	copy(out, s.Confirmations)
	asked := s.asker(callID)
	if j := s.confirmation(presented); presented != "" && j >= 0 {
		if out[j].open() {
			out[j].RefusedBy = callID
		}
		// The fresh nonce asks the same question, whoever's turn the
		// refused try was made in: Alice's yes to Teagan's question must not
		// make it Alice's question.
		asked = out[j].AskedBy
	}
	return append(out, Confirmation{Nonce: nonce, CallID: callID, Tool: call.Tool, Args: args, AskedBy: asked}), true
}

// asker is who the log attributes the turn that made call callID to: the
// speaker of the last thing heard before the call, or the conversation's
// speaker for a call no utterance preceded.
func (s State) asker(callID string) string {
	for i := len(s.Dialogue) - 1; i >= 0; i-- {
		if s.Dialogue[i].Kind == EntryCall && s.Dialogue[i].CallID == callID {
			for j := i - 1; j >= 0; j-- {
				if s.Dialogue[j].Kind == EntryHeard {
					return s.Dialogue[j].Speaker
				}
			}
			break
		}
	}
	return s.Speaker
}

// given closes a confirmation on the call that redeemed it.
func (s State) given(callID, nonce string) ([]Confirmation, bool) {
	i := s.confirmation(nonce)
	if i < 0 {
		return nil, false
	}
	out := make([]Confirmation, len(s.Confirmations))
	copy(out, s.Confirmations)
	out[i].RedeemedBy = callID
	return out, true
}

// answered counts an utterance against every open confirmation; the first
// one after the question is the answer. speaker is who the log attributes
// it to (Attribute), and match how the voice matched.
func (s State) answered(text, speaker, match string) []Confirmation {
	if len(s.Confirmations) == 0 {
		return s.Confirmations
	}
	out := make([]Confirmation, len(s.Confirmations))
	copy(out, s.Confirmations)
	for i := range out {
		if !out[i].open() {
			continue
		}
		out[i].Heard++
		if out[i].Heard == 1 {
			out[i].Answer, out[i].AnsweredBy, out[i].AnswerMatch = text, speaker, match
		}
	}
	return out
}
