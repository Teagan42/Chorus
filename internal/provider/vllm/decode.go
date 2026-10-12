package vllm

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/teagan42/chorus/internal/provider/chat"
	"github.com/teagan42/chorus/internal/session"
)

// maxLine bounds one SSE line. A chunk carries a few tokens, so this is
// generous; it is here so a server that never sends a newline cannot grow
// the buffer without end.
const maxLine = 1 << 20

// finishReasons are the ones a turn may end with. The journal's enum is
// these and error (schema/event.cue); anything else vLLM says, such as
// abort, is a turn the endpoint never finished.
var finishReasons = map[string]bool{"stop": true, "length": true, "tool_calls": true}

// chunk is one `data:` event of a streamed /v1/chat/completions.
type chunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
			// Reasoning is what --reasoning-parser separates from content.
			// Older servers name the field reasoning_content, newer ones
			// reasoning; either is read.
			ReasoningContent string     `json:"reasoning_content"`
			Reasoning        string     `json:"reasoning"`
			ToolCalls        []wireCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`

	// Error is how vLLM reports a failure after the 200 has been sent:
	// {"error": {"message": ...}}. Older servers sent the error object
	// itself, {"object": "error", "message": ...}.
	Error   *apiError `json:"error"`
	Object  string    `json:"object"`
	Message string    `json:"message"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// completion is the assistant message reassembled across the stream, which
// the journal keeps verbatim because replay cannot regenerate it (SPEC §8).
// Its shape is the message vLLM would have returned unstreamed, so a reader
// of the log sees each call's arguments as the model wrote them.
type completion struct {
	Content   string     `json:"content,omitempty"`
	Reasoning string     `json:"reasoning,omitempty"`
	ToolCalls []wireCall `json:"tool_calls,omitempty"`

	// Error is why the stream stopped early, recorded beside the partial
	// output. A failure is an event in its own right, not an absence (SPEC §7).
	Error string `json:"error,omitempty"`
}

// speakArgs is the speak tool's own schema (schema/tool.cue).
type speakArgs struct {
	Text string `json:"text"`
	Mode string `json:"mode"`
}

// call is one tool call as it assembles, keyed by its index in the stream.
type call struct {
	index int
	id    string
	name  string
	args  strings.Builder
	// done is set once the call has been dispatched, or its utterance
	// closed: nothing it receives after that changes what was acted on.
	done bool

	// A speak call streams its words as they arrive (SPEC §4.1): said is
	// what has been emitted, mode what it was emitted under, and open
	// whether an utterance has begun that its last delta must close.
	said string
	mode session.Mode
	open bool
}

// decoder turns one /v1/chat/completions stream into actions, in arrival
// order. Nothing is reordered: a call dispatches the moment its arguments
// close, and a speak call's words stream as they arrive (SPEC §4.1).
type decoder struct{}

// decode reads the stream until the model finishes, emitting actions as
// they arrive.
func (decoder) decode(ctx context.Context, r io.Reader, out chan<- session.Action) (err error) {
	t := &turn{ctx: ctx, out: out, calls: map[int]*call{}}
	defer func() {
		// Closing events are sent on a context that cannot be cancelled.
		// This path is reached *because* the turn was cut off, and a
		// barge-in cancelling it is the ordinary way that happens; a select
		// between the send and ctx.Done() drops them, exactly when they
		// matter. Safe because the caller drains until the channel closes,
		// which Engine.Turn requires of it.
		closing := context.WithoutCancel(ctx)

		// An utterance with no last delta keeps the speech channel busy for
		// as long as the session lives (internal/session/speechchan.go).
		for _, c := range t.ordered() {
			if c.open && !c.done {
				c.done = true
				if cerr := emit(closing, out, session.SpeechDelta{CallID: c.id, Mode: c.mode, Last: true}); err == nil {
					err = cerr
				}
			}
		}
		// Turn reports a mid-stream failure nowhere else: its error return
		// is spent before the first event arrives. So the turn ends here
		// with what it managed to produce and why it stopped (SPEC §8).
		if t.ended || err == nil {
			return
		}
		t.comp.Error = err.Error()
		t.comp.ToolCalls = t.recorded()
		raw, merr := json.Marshal(t.comp)
		if merr != nil {
			return
		}
		_ = emit(closing, out, session.TurnEnd{FinishReason: session.FinishError, Completion: string(raw), Error: t.comp.Error})
	}()

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	var data []string
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line != "" {
			// A comment starts with a colon; an event may carry several
			// data lines, joined by newlines (WHATWG server-sent events).
			if v, ok := strings.CutPrefix(line, "data:"); ok {
				data = append(data, strings.TrimPrefix(v, " "))
			}
			continue
		}
		if len(data) == 0 {
			continue
		}
		payload := strings.Join(data, "\n")
		data = data[:0]
		if payload == "[DONE]" {
			// The server's end of stream, before the model said why it
			// stopped: a turn that never finished.
			return errors.New("vllm: the stream ended before the model finished")
		}
		if err := t.event([]byte(payload)); err != nil {
			return err
		}
		if t.ended {
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read chat stream: %w", err)
	}
	// A stream that stops without a finish reason is a dropped connection,
	// not a turn.
	return io.ErrUnexpectedEOF
}

// turn is one decode's state.
type turn struct {
	ctx   context.Context
	out   chan<- session.Action
	comp  completion
	calls map[int]*call
	ended bool
}

// event applies one chunk.
func (t *turn) event(payload []byte) error {
	var c chunk
	if err := json.Unmarshal(payload, &c); err != nil {
		return fmt.Errorf("decode chat chunk: %w", err)
	}
	if c.Error != nil {
		return fmt.Errorf("vllm: %s", c.Error.Message)
	}
	if c.Object == "error" {
		return fmt.Errorf("vllm: %s", c.Message)
	}
	// One choice: chorusd never asks for n > 1.
	if len(c.Choices) == 0 {
		return nil
	}
	ch := c.Choices[0]
	t.comp.Content += ch.Delta.Content
	t.comp.Reasoning += ch.Delta.ReasoningContent + ch.Delta.Reasoning
	for _, d := range ch.Delta.ToolCalls {
		cl, err := t.arrive(d)
		if err != nil {
			return err
		}
		if err := t.progress(cl); err != nil {
			return err
		}
	}
	if ch.FinishReason == nil {
		return nil
	}
	return t.finish(*ch.FinishReason)
}

// arrive files a tool-call delta under its index. A call's first delta
// carries its id and name; the rest carry argument fragments. A new index
// means every call before it has had all its arguments, since vLLM's tool
// parsers stream one call at a time, so those are closed first.
func (t *turn) arrive(d wireCall) (*call, error) {
	i := 0
	if d.Index != nil {
		i = *d.Index
	}
	cl, ok := t.calls[i]
	if !ok {
		for _, prev := range t.ordered() {
			if prev.index < i {
				if err := t.close(prev); err != nil {
					return nil, err
				}
			}
		}
		cl = &call{index: i, id: d.ID}
		if cl.id == "" {
			// vLLM always names a call; an id is made up only so that a
			// server that does not still has its result correlated.
			cl.id = "call_" + newID()
		}
		t.calls[i] = cl
	}
	if cl.name == "" {
		cl.name = d.Function.Name
	}
	if !cl.done {
		cl.args.WriteString(d.Function.Arguments)
	}
	return cl, nil
}

// progress acts on a call as far as its arguments allow: a speak call says
// the words that have arrived, and any call whose JSON has closed is
// dispatched now, not at the end of the turn (SPEC §4.1).
func (t *turn) progress(cl *call) error {
	if cl.done {
		return nil
	}
	args := cl.args.String()
	if closed(args) {
		// The last of a speak call's words go with its last delta.
		return t.close(cl)
	}
	if cl.name == chat.Speak {
		so := readSpeak(args)
		if so.Started && len(so.Text) > len(cl.said) && strings.HasPrefix(so.Text, cl.said) {
			if !cl.open {
				// The mode is the one declared before the words began, or
				// queue: an utterance is placed in the channel by its first
				// delta, and a mode written after its words is too late to
				// move them (ADR-0077).
				cl.mode = session.ModeQueue
				if so.ModeKnown {
					cl.mode = chat.Mode(so.Mode)
				}
				cl.open = true
			}
			delta := so.Text[len(cl.said):]
			cl.said = so.Text
			if err := emit(t.ctx, t.out, session.SpeechDelta{CallID: cl.id, Text: delta, Mode: cl.mode}); err != nil {
				return err
			}
		}
	}
	return nil
}

// closed reports whether arguments are a whole JSON object: one that has
// closed cannot be extended, so nothing after it can change the call.
func closed(args string) bool {
	trimmed := strings.TrimSpace(args)
	return strings.HasPrefix(trimmed, "{") && json.Valid([]byte(trimmed))
}

// close acts on a call whose arguments are as complete as they will get:
// another call has begun after it, the turn finished, or its JSON closed.
func (t *turn) close(cl *call) error {
	if cl.done {
		return nil
	}
	cl.done = true
	args := strings.TrimSpace(cl.args.String())
	if cl.name != chat.Speak {
		if args == "" {
			// A tool with no parameters, sent with none.
			args = "{}"
			cl.args.Reset()
			cl.args.WriteString(args)
		}
		// Broken JSON goes as written: the call fails as a result the model
		// is shown, and the dialogue sends it back as {} (ADR-0037).
		return emit(t.ctx, t.out, session.ToolCall{ID: cl.id, Tool: cl.name, Args: args})
	}
	var a speakArgs
	if err := json.Unmarshal([]byte(args), &a); err != nil {
		if !cl.open {
			// Nothing was said and nothing can be: the speak call is broken.
			return fmt.Errorf("decode speak args for %s: %w", cl.id, err)
		}
		// Cut off mid-words, by the length limit or the next call: what
		// arrived was said, and the utterance ends there.
		return emit(t.ctx, t.out, session.SpeechDelta{CallID: cl.id, Mode: cl.mode, Last: true})
	}
	// The rest of the words. A reread that ever disagreed with the whole
	// would have said something else already; saying it again is worse.
	rest := ""
	if strings.HasPrefix(a.Text, cl.said) {
		rest = a.Text[len(cl.said):]
	}
	if !cl.open {
		cl.mode, cl.open = chat.Mode(a.Mode), true
	}
	cl.said = a.Text
	return emit(t.ctx, t.out, session.SpeechDelta{CallID: cl.id, Text: rest, Mode: cl.mode, Last: true})
}

// finish ends the turn: every call still open is closed, a turn that
// called nothing has its answer spoken (ADR-0046), and TurnEnd carries the
// completion.
func (t *turn) finish(reason string) error {
	for _, cl := range t.ordered() {
		if err := t.close(cl); err != nil {
			return err
		}
	}
	t.comp.ToolCalls = t.recorded()
	if !finishReasons[reason] {
		return fmt.Errorf("vllm: finished with %q", reason)
	}
	text := chat.Answer(t.comp.Content, len(t.calls) > 0, t.comp.Reasoning != "", reason)
	if text != "" {
		if err := emit(t.ctx, t.out, session.SpeechDelta{Text: text, Mode: session.ModeQueue, Last: true}); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(t.comp)
	if err != nil {
		return fmt.Errorf("encode completion: %w", err)
	}
	t.ended = true
	// The model finished, so the turn says so even if it was cancelled in the
	// meantime: end_session closing the session is the ordinary way that
	// happens, and the completion is the journal's (session.Engine).
	return emit(context.WithoutCancel(t.ctx), t.out, session.TurnEnd{FinishReason: reason, Completion: string(raw)})
}

// ordered is the calls by index, the order they arrived in.
func (t *turn) ordered() []*call {
	out := make([]*call, 0, len(t.calls))
	for _, cl := range t.calls {
		out = append(out, cl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].index < out[j].index })
	return out
}

// recorded is the calls as the completion keeps them.
func (t *turn) recorded() []wireCall {
	var out []wireCall
	for _, cl := range t.ordered() {
		var w wireCall
		w.ID, w.Type = cl.id, "function"
		w.Function.Name, w.Function.Arguments = cl.name, cl.args.String()
		out = append(out, w)
	}
	return out
}

func emit(ctx context.Context, out chan<- session.Action, a session.Action) error {
	select {
	case out <- a:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
