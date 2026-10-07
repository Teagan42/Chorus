package ollama

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/teaganglenn/chorus/internal/session"
)

// toolSpeak is the one tool that is speech rather than an action. The model
// speaks only by calling it, which is the framing that survives the
// speech-to-speech adapter (SPEC §12, §4.1).
const toolSpeak = "speak"

// maxLine bounds one NDJSON line. A whole utterance arrives in a single line,
// so the default scanner limit is not enough.
const maxLine = 1 << 20

// finishError marks a turn the endpoint never finished. It cannot collide with
// a real done_reason, which this endpoint only ever reports as stop or length.
const finishError = "error"

// chunk is one NDJSON line of a /api/chat stream.
type chunk struct {
	Message struct {
		Content string `json:"content"`
		// Thinking is reasoning the endpoint separates from Content. Not every
		// model uses it: some emit reasoning as Content instead, which is why
		// speakInlineContent exists and defaults to off.
		Thinking  string     `json:"thinking"`
		ToolCalls []toolCall `json:"tool_calls"`
	} `json:"message"`
	Done       bool   `json:"done"`
	DoneReason string `json:"done_reason"`
	Error      string `json:"error"`
}

// toolCall is one call as this endpoint frames it.
type toolCall struct {
	ID       string `json:"id,omitempty"`
	Function struct {
		Index int    `json:"index"`
		Name  string `json:"name"`
		// Arguments is a native JSON object, not the string the OpenAI shape
		// uses, so no fragment reassembly is needed.
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// completion is the assistant message reassembled across the stream, which the
// journal keeps verbatim because replay cannot regenerate it (SPEC §8).
//
// The whole message, not just content: a turn is often nothing but tool calls,
// and content alone would record those turns as empty. Marshalling always
// yields at least "{}", which matters because completion_json is a required
// journal field and an empty one fails the turn (internal/journal/journal.go).
type completion struct {
	Content   string     `json:"content,omitempty"`
	Thinking  string     `json:"thinking,omitempty"`
	ToolCalls []toolCall `json:"tool_calls,omitempty"`

	// Error is why the stream stopped early, recorded beside the partial
	// output. A failure is an event in its own right, not an absence (SPEC §7).
	Error string `json:"error,omitempty"`
}

// speakArgs is the speak tool's own schema (schema/tool.cue).
type speakArgs struct {
	Text string `json:"text"`
	Mode string `json:"mode"`
}

// decoder turns one /api/chat stream into actions, in arrival order.
//
// Order is preserved rather than corrected. A model that emits its slow tool
// before the speak meant to cover it cannot be fixed here: calls arrive on
// separate lines, so reordering means buffering to end of turn, which defeats
// the streaming dispatch this package exists to provide (SPEC §4.1).
type decoder struct {
	// speakInlineContent treats message.content as an implicit speak, which
	// SPEC §4.1 requires for templates that emit content beside tool_calls.
	//
	// Off unless a model is known to qualify, because the clause's condition is
	// a property of the template and most local models fail it: with
	// think:false, qwen3:4b streams 2159 chars of "Okay, the user is asking
	// about the weather..." as content and never calls a tool. Speaking that
	// reads the model's private reasoning into the room.
	speakInlineContent bool
}

// decode reads the stream until done, emitting actions as they arrive.
func (d decoder) decode(ctx context.Context, r io.Reader, out chan<- session.Action) (err error) {
	var comp completion
	// inlineOpen tracks an implicit utterance that has no closing delta yet;
	// ended tracks whether the turn already reported its own end.
	var inlineOpen, ended bool
	defer func() {
		// Closing events are sent on a context that cannot be cancelled. This
		// path is reached *because* the turn was cut off, and cancellation is
		// the ordinary way that happens -- barge-in cancels the turn -- so a
		// select between the send and ctx.Done() drops them about half the
		// time, exactly when they matter. Safe because the caller drains until
		// the channel closes, which Engine.Turn requires of it.
		closing := context.WithoutCancel(ctx)

		// Inline content streams with no end marker of its own, and Last is
		// what closes the utterance (internal/session/speechchan.go). A stream
		// that dies mid-utterance must still close it: speechChannel.play waits
		// on the session context, so an unclosed one blocks waitIdle for the
		// whole session.
		if inlineOpen {
			if cerr := emit(closing, out, session.SpeechDelta{Mode: session.ModeQueue, Last: true}); err == nil {
				err = cerr
			}
		}
		// Turn reports a mid-stream failure nowhere else: its error return is
		// spent before the first chunk arrives, and the session only reads the
		// action channel. So the turn ends here with what it managed to produce
		// and why it stopped, rather than vanishing from the log (SPEC §8).
		if ended || err == nil {
			return
		}
		comp.Error = err.Error()
		raw, merr := json.Marshal(comp)
		if merr != nil {
			return
		}
		_ = emit(closing, out, session.TurnEnd{FinishReason: finishError, Completion: string(raw)})
	}()

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var c chunk
		if err := json.Unmarshal(line, &c); err != nil {
			return fmt.Errorf("decode chat chunk: %w", err)
		}
		if c.Error != "" {
			return fmt.Errorf("ollama: %s", c.Error)
		}
		comp.Content += c.Message.Content
		comp.Thinking += c.Message.Thinking
		comp.ToolCalls = append(comp.ToolCalls, c.Message.ToolCalls...)

		if text := c.Message.Content; text != "" && d.speakInlineContent {
			// No call id: the session records it as an implicit speak
			// (internal/session/session.go).
			if err := emit(ctx, out, session.SpeechDelta{Text: text, Mode: session.ModeQueue}); err != nil {
				return err
			}
			inlineOpen = true
		}
		for _, tc := range c.Message.ToolCalls {
			act, err := action(tc.ID, tc.Function.Name, tc.Function.Arguments)
			if err != nil {
				return err
			}
			if err := emit(ctx, out, act); err != nil {
				return err
			}
		}
		if c.Done {
			// Closed before the turn ends so the journal reads in the order the
			// session heard it, rather than from the deferred close above.
			if inlineOpen {
				inlineOpen = false
				if err := emit(ctx, out, session.SpeechDelta{Mode: session.ModeQueue, Last: true}); err != nil {
					return err
				}
			}
			raw, err := json.Marshal(comp)
			if err != nil {
				return fmt.Errorf("encode completion: %w", err)
			}
			ended = true
			// done_reason is "stop" even for a tool call: this endpoint has no
			// tool_calls equivalent, so nothing may infer tool use from it.
			return emit(ctx, out, session.TurnEnd{
				FinishReason: c.DoneReason,
				Completion:   string(raw),
			})
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read chat stream: %w", err)
	}
	// A stream that stops without done is a dropped connection, not a turn.
	return io.ErrUnexpectedEOF
}

// action maps one tool call onto the action it represents.
func action(id, name string, args json.RawMessage) (session.Action, error) {
	if name != toolSpeak {
		return session.ToolCall{ID: id, Tool: name, Args: string(args)}, nil
	}
	var a speakArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("decode speak args for %s: %w", id, err)
	}
	// One call carries a whole utterance: arguments never arrive incrementally
	// on this endpoint, so every delta is also the last one.
	return session.SpeechDelta{
		CallID: id,
		Text:   a.Text,
		Mode:   mode(a.Mode),
		Last:   true,
	}, nil
}

// mode keeps an unrecognised mode from reaching the speech channel. The schema
// declares a closed enum, but Ollama does not validate arguments against it:
// qwen3:14b emits mode="filler" when the prompt suggests one. Queue is the only
// safe default, since preempt and interject discard or duck speech the user is
// still hearing (SPEC §4.2).
//
// This cannot catch a mode that is valid but wrong -- ornith:9b chose preempt
// to acknowledge a request, cutting off the speech it should have queued behind.
// Only the prompt can fix that.
func mode(s string) session.Mode {
	switch m := session.Mode(s); m {
	case session.ModeQueue, session.ModePreempt, session.ModeInterject:
		return m
	default:
		return session.ModeQueue
	}
}

func emit(ctx context.Context, out chan<- session.Action, a session.Action) error {
	select {
	case out <- a:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
