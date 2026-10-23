package otel

import (
	"encoding/json"
	"slices"
	"strconv"
	"time"

	"github.com/teagan42/chorus/internal/journal"
)

// Bounds on what a live builder holds. A conversation or call that never
// ends is let go, oldest first, as an unfinished span.
const (
	maxConversations = 256
	maxOpenCalls     = 256
)

// Options says what the spans carry beyond structure and timing.
type Options struct {
	// Words puts what was said, heard, generated and passed to tools on the
	// spans. Off, a span says who, where, which tool and how it went, never
	// the words: a trace backend is one more place a home's speech would be
	// kept (ADR-0104).
	Words bool
}

// Builder turns a stream of journal events into spans, one event at a time.
// It is a reader of the log, never a writer (ADR-0007).
type Builder struct {
	opts  Options
	convs map[string]*conv
	// order is the conversations by last event, least recent first.
	order []string
}

// NewBuilder returns a builder holding nothing open.
func NewBuilder(o Options) *Builder {
	return &Builder{opts: o, convs: map[string]*conv{}}
}

// Spans is every span a whole log yields, the still-open ones ended at the
// last event that touched them and marked unfinished. Reading a log twice
// yields the same spans, ids included.
func Spans(events []journal.Event, o Options) []Span {
	b := NewBuilder(o)
	var out []Span
	for _, e := range events {
		out = append(out, b.Add(e)...)
	}
	return append(out, b.Unfinished()...)
}

// Add folds one stored event in and returns the spans it ended.
func (b *Builder) Add(e journal.Event) []Span {
	h := handlers[e.Kind]
	if h == nil {
		return nil
	}
	c, out := b.conv(e.ConversationID)
	out = append(out, h(b, c, e)...)
	if e.Kind == journal.KindSpeculationDiscarded {
		// A guess at the next utterance is no part of the turn still open.
		c.touchSession(e.At)
	} else {
		c.touch(e.At)
	}
	return out
}

// Unfinished ends every open span where it was last touched, and forgets it.
func (b *Builder) Unfinished() []Span {
	var out []Span
	for _, id := range b.order {
		out = append(out, b.convs[id].unfinished()...)
	}
	b.convs, b.order = map[string]*conv{}, nil
	return out
}

// conv is the conversation's state, made on its first event. Making one may
// let the least recent go, and returns what it held.
func (b *Builder) conv(id string) (*conv, []Span) {
	if i := slices.Index(b.order, id); i >= 0 {
		b.order = append(slices.Delete(b.order, i, i+1), id)
		return b.convs[id], nil
	}
	var out []Span
	if len(b.order) == maxConversations {
		out = b.convs[b.order[0]].unfinished()
		delete(b.convs, b.order[0])
		b.order = b.order[1:]
	}
	c := &conv{id: id, trace: traceOf(id), tools: map[string]*open{}, timedOut: map[string]timedOut{}}
	b.convs[id] = c
	b.order = append(b.order, id)
	return c, out
}

// conv is what one conversation has open.
type conv struct {
	id    string
	trace TraceID

	// sessions are open, oldest first: a migration opens the next before the
	// last is closed only by a moment, if at all (SPEC §4.5).
	sessions []*open
	// closed is the session closed last, which a summary written after
	// the close is part of (ADR-0043).
	closed   SpanID
	closedAt time.Time

	turn  *open
	model *open
	// ask is where the next model span starts: the utterance, or the
	// completion or result the follow-up ask was made after. Nil outside a
	// turn and after a hot phrase, which no model answers.
	ask *anchor

	tools map[string]*open
	calls []string
	// timedOut is each call given up on, whose detached work may still
	// report (ADR-0060).
	timedOut map[string]timedOut
}

type anchor struct {
	seq uint64
	at  time.Time
}

type timedOut struct {
	span SpanID
	tool string
	at   time.Time
}

// open is a span not yet ended.
type open struct {
	Span
	// seen is when the last event that touched it was stored.
	seen time.Time
	// satellite pairs a session's close with its open; tool names a call.
	satellite, tool string
	// firstAudio is set once the turn's first frame has been heard.
	firstAudio bool
}

func (c *conv) start(e journal.Event, role, name string, kind SpanKind, parent SpanID) *open {
	return &open{Span: Span{
		Trace: c.trace, ID: spanOf(c.id, e.Seq, role), Parent: parent,
		Name: name, Kind: kind, Start: e.At,
		Attrs: []Attr{
			String("chorus.conversation_id", c.id),
			String("session.id", c.id),
			Int("chorus.seq", int64(e.Seq)),
		},
	}, seen: e.At}
}

// session is the newest open session, or nil.
func (c *conv) session() *open {
	if len(c.sessions) == 0 {
		return nil
	}
	return c.sessions[len(c.sessions)-1]
}

// within is the innermost of the turn and the session, where a moment that
// belongs to neither a call nor an ask is recorded.
func (c *conv) within() *open {
	if c.turn != nil {
		return c.turn
	}
	return c.session()
}

func idOf(o *open) SpanID {
	if o == nil {
		return SpanID{}
	}
	return o.ID
}

func (c *conv) touch(at time.Time) {
	for _, o := range []*open{c.session(), c.turn, c.model} {
		o.saw(at)
	}
}

func (c *conv) touchSession(at time.Time) { c.session().saw(at) }

func (o *open) saw(at time.Time) {
	if o != nil && at.After(o.seen) {
		o.seen = at
	}
}

// askModel is the model span the turn's current ask is, opened at its
// anchor the first time something shows the model answering.
func (c *conv) askModel(e journal.Event) *open {
	if c.model != nil || c.ask == nil {
		return c.model
	}
	m := c.start(journal.Event{Seq: c.ask.seq, At: c.ask.at}, "model", "chat "+e.Versions.Model, KindClient, idOf(c.within()))
	m.Attrs = append(m.Attrs,
		String("gen_ai.operation.name", "chat"),
		String("gen_ai.request.model", e.Versions.Model),
		String("gen_ai.conversation.id", c.id),
		String("chorus.prompt_version", e.Versions.Prompt),
		String("chorus.tool_schema_version", e.Versions.ToolSchema),
	)
	c.model = m
	return m
}

// endTurn ends the turn where its last event was, and an ask still open in
// it: one the next utterance cut before its completion was recorded.
func (c *conv) endTurn() []Span {
	var out []Span
	if m := c.model; m != nil {
		m.Attrs = append(m.Attrs, Bool("chorus.cut", true))
		out = append(out, m.end(m.seen))
	}
	if t := c.turn; t != nil {
		out = append(out, t.end(t.seen))
	}
	c.turn, c.model, c.ask = nil, nil, nil
	return out
}

// newestSpeech is the speak call most recently opened and still playing.
func (c *conv) newestSpeech() *open {
	for _, id := range slices.Backward(c.calls) {
		if t := c.tools[id]; t != nil && t.tool == "speak" {
			return t
		}
	}
	return nil
}

func (c *conv) dropCall(id string) {
	delete(c.tools, id)
	c.calls = slices.DeleteFunc(c.calls, func(s string) bool { return s == id })
}

func (o *open) end(at time.Time) Span {
	s := o.Span
	s.End = at
	if s.End.Before(s.Start) {
		s.End = s.Start
	}
	return s
}

func (o *open) fail(msg string) { o.Status = Status{Failed: true, Message: msg} }

func (o *open) event(e journal.Event, attrs ...Attr) {
	o.Events = append(o.Events, Event{Name: string(e.Kind), At: e.At, Attrs: attrs})
	o.saw(e.At)
}

// unfinished ends everything the conversation has open, innermost first.
func (c *conv) unfinished() []Span {
	var out []Span
	mark := func(o *open) {
		o.Attrs = append(o.Attrs, Bool("chorus.unfinished", true))
		out = append(out, o.end(o.seen))
	}
	for _, id := range c.calls {
		mark(c.tools[id])
	}
	for _, o := range []*open{c.model, c.turn} {
		if o != nil {
			mark(o)
		}
	}
	for _, o := range slices.Backward(c.sessions) {
		mark(o)
	}
	return out
}

// handler folds one kind of event into its conversation.
type handler func(b *Builder, c *conv, e journal.Event) []Span

// handlers says what each kind of event is to a trace. A nil entry is a
// kind no trace shows: the device's and the house's own logs, which no
// conversation holds. Every kind is listed, so a new one is a decision.
var handlers = map[journal.Kind]handler{
	journal.KindSessionOpened:          sessionOpened,
	journal.KindSessionClosed:          sessionClosed,
	journal.KindUtteranceTranscribed:   utterance,
	journal.KindModelCompleted:         modelCompleted,
	journal.KindModelFailed:            modelFailed,
	journal.KindToolCalled:             toolCalled,
	journal.KindToolResult:             toolResult,
	journal.KindSpeechStarted:          speechStarted,
	journal.KindSpeechSpoken:           speechSpoken,
	journal.KindSpeechTruncated:        speechTruncated,
	journal.KindSpeechFailed:           speechFailed,
	journal.KindSpeechDiscarded:        speechDiscarded,
	journal.KindBargeInDetected:        bargeIn,
	journal.KindBargeInRejected:        bargeIn,
	journal.KindMemoryRecalled:         memoryRecalled,
	journal.KindConfirmationRequested:  confirmation,
	journal.KindConfirmationGiven:      confirmation,
	journal.KindAnnouncementMade:       announcement,
	journal.KindSpeculationDiscarded:   speculation,
	journal.KindConversationSummarized: summarized,
	journal.KindAudioDropped:           audioDropped,

	journal.KindPresenceChanged: nil,
	journal.KindWakeRejected:    nil,
	journal.KindTimerStarted:    nil,
	journal.KindTimerCancelled:  nil,
	journal.KindTimerFinished:   nil,
}

// fields reads an event's fields into attributes, skipping empty ones.
type fields struct {
	e     journal.Event
	attrs []Attr
}

func (f *fields) str(field, key string) *fields {
	if v := f.e.Fields[field]; v != "" {
		f.attrs = append(f.attrs, String(key, v))
	}
	return f
}

func (f *fields) integer(field, key string) *fields {
	if v, err := strconv.ParseInt(f.e.Fields[field], 10, 64); err == nil {
		f.attrs = append(f.attrs, Int(key, v))
	}
	return f
}

func (f *fields) number(field, key string) *fields {
	if v, err := strconv.ParseFloat(f.e.Fields[field], 64); err == nil {
		f.attrs = append(f.attrs, Float(key, v))
	}
	return f
}

func (f *fields) boolean(field, key string) *fields {
	if v, err := strconv.ParseBool(f.e.Fields[field]); err == nil {
		f.attrs = append(f.attrs, Bool(key, v))
	}
	return f
}

// words reads a field only when the spans carry words.
func (f *fields) words(b *Builder, field, key string) *fields {
	if b.opts.Words {
		f.str(field, key)
	}
	return f
}

func read(e journal.Event) *fields { return &fields{e: e} }

func sessionOpened(_ *Builder, c *conv, e journal.Event) []Span {
	s := c.start(e, "session", "chorus.session", KindInternal, SpanID{})
	s.satellite = e.Fields["satellite"]
	s.Attrs = append(s.Attrs, read(e).
		str("satellite", "chorus.satellite").
		str("room", "chorus.room").
		str("speaker_id", "chorus.speaker_id").
		str("wake_word", "chorus.wake_word").
		number("wake_confidence", "chorus.wake_confidence").
		boolean("resumed", "chorus.resumed").
		boolean("announced", "chorus.announced").attrs...)
	c.sessions = append(c.sessions, s)
	return nil
}

func sessionClosed(_ *Builder, c *conv, e journal.Event) []Span {
	i := slices.IndexFunc(c.sessions, func(o *open) bool { return o.satellite == e.Fields["satellite"] })
	if i < 0 {
		return nil
	}
	s := c.sessions[i]
	var out []Span
	if c.turn != nil && c.turn.Parent == s.ID {
		out = c.endTurn()
	}
	reason := e.Fields["reason"]
	s.Attrs = append(s.Attrs, String("chorus.close_reason", reason))
	if reason == "error" || reason == "device_lost" {
		s.fail(reason)
	}
	c.sessions = slices.Delete(c.sessions, i, i+1)
	c.closed, c.closedAt = s.ID, e.At
	return append(out, s.end(e.At))
}

func utterance(b *Builder, c *conv, e journal.Event) []Span {
	out := c.endTurn()
	t := c.start(e, "turn", "chorus.turn", KindInternal, idOf(c.session()))
	t.Attrs = append(t.Attrs, read(e).
		str("speaker_id", "chorus.speaker_id").
		str("speaker_id", "user.id").
		str("speaker_match", "chorus.speaker_match").
		str("turn_end", "chorus.turn_end").
		number("turn_probability", "chorus.turn_probability").
		str("hot_word", "chorus.hot_word").
		words(b, "text", "chorus.transcript").attrs...)
	c.turn = t
	if e.Fields["hot_word"] == "" {
		c.ask = &anchor{seq: e.Seq, at: e.At}
	}
	return out
}

func modelCompleted(b *Builder, c *conv, e journal.Event) []Span {
	if c.ask == nil {
		c.ask = &anchor{seq: e.Seq, at: e.At}
	}
	m := c.askModel(e)
	f := read(e).integer("asked_early_ms", "chorus.asked_early_ms").words(b, "completion_json", "chorus.completion_json")
	m.Attrs = append(m.Attrs, f.attrs...)
	reason := e.Fields["finish_reason"]
	m.Attrs = append(m.Attrs, Strings("gen_ai.response.finish_reasons", reason))
	if reason == "error" {
		m.fail("finish_reason error")
	}
	// Asked in the pause and kept: the ask began before its utterance ended.
	if early, err := strconv.ParseInt(e.Fields["asked_early_ms"], 10, 64); err == nil {
		m.Start = m.Start.Add(-time.Duration(early) * time.Millisecond)
		if c.turn != nil && m.Start.Before(c.turn.Start) {
			c.turn.Start = m.Start
		}
	}
	c.model, c.ask = nil, &anchor{seq: e.Seq, at: e.At}
	return []Span{m.end(e.At)}
}

func modelFailed(_ *Builder, c *conv, e journal.Event) []Span {
	if c.ask == nil {
		c.ask = &anchor{seq: e.Seq, at: e.At}
	}
	m := c.askModel(e)
	reason := e.Fields["reason"]
	m.Attrs = append(m.Attrs, read(e).str("reason", "error.type").str("canned_call_id", "chorus.canned_call_id").attrs...)
	m.fail(reason + ": " + e.Fields["error"])
	// No model is left to ask again: what follows is the canned line.
	c.model, c.ask = nil, nil
	return []Span{m.end(e.At)}
}

func toolCalled(b *Builder, c *conv, e journal.Event) []Span {
	parent := idOf(c.askModel(e))
	if parent.IsZero() {
		parent = idOf(c.within())
	}
	tool, id := e.Fields["tool"], e.Fields["call_id"]
	t := c.start(e, "tool", "execute_tool "+tool, KindInternal, parent)
	t.tool = tool
	t.Attrs = append(t.Attrs, String("gen_ai.operation.name", "execute_tool"), String("gen_ai.conversation.id", c.id))
	t.Attrs = append(t.Attrs, read(e).
		str("tool", "gen_ai.tool.name").
		str("call_id", "gen_ai.tool.call.id").
		words(b, "args_json", "gen_ai.tool.call.arguments").attrs...)
	var out []Span
	if old := c.tools[id]; old != nil {
		old.Attrs = append(old.Attrs, Bool("chorus.unfinished", true))
		out = append(out, old.end(old.seen))
		c.dropCall(id)
	}
	if len(c.calls) == maxOpenCalls {
		oldest := c.tools[c.calls[0]]
		oldest.Attrs = append(oldest.Attrs, Bool("chorus.unfinished", true))
		out = append(out, oldest.end(oldest.seen))
		c.dropCall(c.calls[0])
	}
	c.tools[id] = t
	c.calls = append(c.calls, id)
	return out
}

func toolResult(b *Builder, c *conv, e journal.Event) []Span {
	id, outcome := e.Fields["call_id"], e.Fields["outcome"]
	if c.model == nil && c.ask != nil {
		// A follow-up ask is made once the results it is told have landed.
		c.ask.at = e.At
	}
	f := read(e).str("outcome", "chorus.tool.outcome").words(b, "result_json", "gen_ai.tool.call.result")
	if t := c.tools[id]; t != nil {
		c.dropCall(id)
		t.Attrs = append(t.Attrs, f.attrs...)
		if (outcome == "error" || outcome == "timed_out") && !t.Status.Failed {
			t.fail(outcome)
		}
		if outcome == "timed_out" {
			c.timedOut[id] = timedOut{span: t.ID, tool: t.tool, at: e.At}
		}
		return []Span{t.end(e.At)}
	}
	gave, ok := c.timedOut[id]
	if !ok {
		return nil
	}
	// The work went on after the call was given up on, and has now reported.
	if outcome != "timed_out" {
		delete(c.timedOut, id)
	}
	d := c.start(e, "detached", "detached "+gave.tool, KindInternal, gave.span)
	d.Start = gave.at
	d.Attrs = append(d.Attrs, String("gen_ai.tool.name", gave.tool), String("gen_ai.tool.call.id", id))
	d.Attrs = append(d.Attrs, f.attrs...)
	if outcome == "error" {
		d.fail(outcome)
	}
	return []Span{d.end(e.At)}
}

func speechStarted(_ *Builder, c *conv, e journal.Event) []Span {
	wait, err := strconv.ParseInt(e.Fields["wait_ms"], 10, 64)
	if t := c.tools[e.Fields["call_id"]]; t != nil {
		t.event(e, read(e).integer("wait_ms", "chorus.wait_ms").attrs...)
		if err == nil {
			t.Attrs = append(t.Attrs, Int("chorus.first_audio.wait_ms", wait))
		}
	}
	// The turn is waited through from when the person stopped speaking.
	if t := c.turn; t != nil && err == nil && !t.firstAudio {
		t.firstAudio = true
		t.Attrs = append(t.Attrs, Int("chorus.first_audio.wait_ms", wait))
		if stopped := e.At.Add(-time.Duration(wait) * time.Millisecond); stopped.Before(t.Start) {
			t.Start = stopped
		}
	}
	return nil
}

func speechSpoken(b *Builder, c *conv, e journal.Event) []Span {
	if t := c.tools[e.Fields["call_id"]]; t != nil {
		f := read(e).integer("frames_played", "chorus.speech.frames_played")
		t.Attrs = append(t.Attrs, f.attrs...)
		t.event(e, f.words(b, "text", "chorus.speech.text").attrs...)
	}
	return nil
}

func speechTruncated(b *Builder, c *conv, e journal.Event) []Span {
	if t := c.tools[e.Fields["call_id"]]; t != nil {
		f := read(e).integer("frames_played", "chorus.speech.frames_played").str("reason", "chorus.speech.cut_reason")
		t.Attrs = append(t.Attrs, f.attrs...)
		t.event(e, f.words(b, "spoken_text", "chorus.speech.text").words(b, "unspoken_text", "chorus.speech.unspoken_text").attrs...)
	}
	return nil
}

func speechFailed(_ *Builder, c *conv, e journal.Event) []Span {
	if t := c.tools[e.Fields["call_id"]]; t != nil {
		t.event(e, read(e).str("reason", "error.type").str("error", "chorus.error").str("canned_call_id", "chorus.canned_call_id").attrs...)
		t.fail(e.Fields["reason"])
	}
	return nil
}

func speechDiscarded(b *Builder, c *conv, e journal.Event) []Span {
	at := c.tools[e.Fields["call_id"]]
	if at == nil {
		at = c.within()
	}
	if at != nil {
		at.event(e, read(e).str("reason", "chorus.reason").str("call_id", "gen_ai.tool.call.id").words(b, "unspoken_text", "chorus.speech.unspoken_text").attrs...)
	}
	return nil
}

// bargeIn is recorded on the speech it would have cut.
func bargeIn(_ *Builder, c *conv, e journal.Event) []Span {
	at := c.newestSpeech()
	if at == nil {
		at = c.within()
	}
	if at != nil {
		at.event(e, read(e).
			integer("tts_position_ms", "chorus.tts_position_ms").
			str("stage", "chorus.stage").
			str("hot_word", "chorus.hot_word").
			boolean("speaker_stage_skipped", "chorus.speaker_stage_skipped").
			integer("audio_frames", "chorus.audio_frames").
			number("echo_score", "chorus.echo_score").
			integer("echo_lag_ms", "chorus.echo_lag_ms").attrs...)
	}
	return nil
}

func memoryRecalled(b *Builder, c *conv, e journal.Event) []Span {
	if at := c.within(); at != nil {
		at.event(e, read(e).
			str("person", "chorus.person").
			str("ranked_by", "chorus.ranked_by").
			words(b, "memories_json", "chorus.memories_json").
			words(b, "summaries_json", "chorus.summaries_json").attrs...)
	}
	return nil
}

func confirmation(_ *Builder, c *conv, e journal.Event) []Span {
	at := c.tools[e.Fields["call_id"]]
	if at == nil {
		at = c.within()
	}
	if at != nil {
		at.event(e, read(e).str("call_id", "gen_ai.tool.call.id").attrs...)
	}
	return nil
}

func announcement(b *Builder, c *conv, e journal.Event) []Span {
	if s := c.session(); s != nil {
		s.event(e, read(e).
			str("source", "chorus.source").
			str("timer_id", "chorus.timer_id").
			str("requested_by", "chorus.requested_by").
			str("from_satellite", "chorus.from_satellite").
			str("from_conversation", "chorus.from_conversation").
			boolean("start_conversation", "chorus.start_conversation").
			words(b, "text", "chorus.speech.text").attrs...)
	}
	return nil
}

// speculation is the ask made in the pause and thrown away: what the guess
// cost, which replay skips (ADR-0095). It guessed at the next utterance, so
// it belongs to the session, not the turn still open.
func speculation(b *Builder, c *conv, e journal.Event) []Span {
	s := c.start(e, "speculation", "chorus.speculation", KindClient, idOf(c.session()))
	if ran, err := strconv.ParseInt(e.Fields["ran_ms"], 10, 64); err == nil {
		s.Start = e.At.Add(-time.Duration(ran) * time.Millisecond)
	}
	s.Attrs = append(s.Attrs, Bool("chorus.speculative", true), String("gen_ai.request.model", e.Versions.Model))
	s.Attrs = append(s.Attrs, read(e).
		str("reason", "chorus.discard_reason").
		str("speaker_id", "chorus.speaker_id").
		str("finish_reason", "chorus.finish_reason").
		integer("ran_ms", "chorus.ran_ms").
		words(b, "text", "chorus.transcript").
		words(b, "completion_json", "chorus.completion_json").attrs...)
	return []Span{s.end(e.At)}
}

// summarized is the summary the model wrote once the conversation ended,
// from the close it followed (ADR-0043).
func summarized(b *Builder, c *conv, e journal.Event) []Span {
	s := c.start(e, "summary", "chorus.summary", KindClient, c.closed)
	if !c.closedAt.IsZero() {
		s.Start = c.closedAt
	}
	var people []string
	if json.Unmarshal([]byte(e.Fields["people_json"]), &people) == nil && len(people) > 0 {
		s.Attrs = append(s.Attrs, Strings("chorus.people", people...))
	}
	s.Attrs = append(s.Attrs, String("gen_ai.request.model", e.Versions.Model))
	s.Attrs = append(s.Attrs, read(e).words(b, "summary", "chorus.summary").attrs...)
	if e.Fields["error"] != "" {
		s.fail(e.Fields["error"])
	}
	return []Span{s.end(e.At)}
}

func audioDropped(_ *Builder, c *conv, e journal.Event) []Span {
	if at := c.within(); at != nil {
		at.event(e, read(e).str("reason", "chorus.reason").str("audio_ref", "chorus.audio_ref").attrs...)
	}
	return nil
}
