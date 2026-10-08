// Package listen is the Listening child of SPEC §4 made real: one per
// satellite link, it turns the device's mic stream into the session's
// utterances, barge-in candidates and speaker attribution. It is the
// bridge.Handler for the uplink; the Speaking side is internal/satellite.
//
// Nothing here blocks the bridge read loop. Audio is appended to the open
// utterance inline, which is what stt.Utterance.Write was built for; every
// slow thing -- the final decode, the embedder, the blob store, the turn --
// runs on a goroutine the utterance owns, and every goroutine exits on the
// link's context (CONTRIBUTING §6, ADR-0030).
package listen

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/teaganglenn/chorus/internal/blob"
	"github.com/teaganglenn/chorus/internal/bridge"
	"github.com/teaganglenn/chorus/internal/identity"
	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/session"
	"github.com/teaganglenn/chorus/internal/stt"
)

// Opener opens a session for a confirmed wake. *session.Supervisor fills it.
type Opener interface {
	Open(ctx context.Context, w session.Wake) (*session.Session, error)
}

// Speakers attributes an utterance. *identity.Resolver fills it; nil means
// everyone is a guest and stage three of wake confirmation is skipped.
type Speakers interface {
	Resolve(ctx context.Context, pcm []byte) (identity.Outcome, error)
}

// Playback is where the speech now playing started on the DAC's cumulative
// count. *satellite.Satellite fills it; nil counts positions from the
// connection instead, which is only right before the first utterance.
type Playback interface {
	SpeechBase() uint64
}

// DefaultWakeWindow is how much audio may follow a wake word before it is
// given up as a false accept: the session's own silence backstop, counted in
// bytes so a test drives it without a clock (session.DefaultSilence).
const DefaultWakeWindow = int(session.DefaultSilence/time.Second) * bytesPerSecond

// DefaultLeadIn is the audio kept from before an utterance's first loud chunk:
// 300 ms, enough that an onset the threshold missed still reaches STT.
const DefaultLeadIn = 300 * bytesPerSecond / 1000

// Config wires one listener. Everything that reads a clock or does I/O is
// injected, so `task test` stays hermetic (CONTRIBUTING §1).
type Config struct {
	// Satellite is the device's name, which the session is opened on.
	Satellite string

	Sessions    Opener
	Transcriber stt.Transcriber
	Speakers    Speakers

	// Playback is the Speaking side of the same link, which owns the origin a
	// candidate's position is measured from.
	Playback Playback

	// Blobs keeps every utterance and candidate the journal refers to.
	Blobs blob.Store

	// Journal takes the wake rejections, which belong to no conversation.
	Journal *journal.Journal

	// Endpointer defaults to NewEnergy.
	Endpointer Endpointer

	// STT tunes each utterance's partial cadence and bound.
	STT stt.Options

	// WakeWindow and LeadIn are in bytes; zero takes the defaults.
	WakeWindow int
	LeadIn     int

	// Log receives what a goroutine with no caller cannot return. Nil discards.
	Log *slog.Logger
}

// DeviceConversation is the log a satellite's wake rejections go to. A
// rejected wake opened no conversation, and the audio stream belongs to the
// device (SPEC §4.5), so the device is what the record is keyed on.
func DeviceConversation(satellite string) string { return "device:" + satellite }

// Listener is one link's Listening child.
type Listener struct {
	cfg  Config
	ctx  context.Context
	log  *slog.Logger
	wg   sync.WaitGroup
	done chan struct{}

	mu     sync.Mutex
	closed bool
	muted  bool
	played uint64
	woken  bool
	// confirming holds from the first utterance's start until it opens the
	// session or rejects the wake, so speech in that gap is still segmented.
	confirming bool
	wake       []byte // audio since the wake, until speech starts
	lead       []byte // audio just before an utterance, prepended to it
	sess       *session.Session
	cur        *utterance
	// last is the newest utterance's ticket. Each utterance waits on the one
	// before it before it is heard, so turns run one at a time, in order.
	last <-chan struct{}
}

// Open validates the wiring and starts the listener. It runs until ctx ends,
// which is the link's Serve lifetime.
func Open(ctx context.Context, cfg Config) (*Listener, error) {
	switch {
	case cfg.Satellite == "":
		return nil, errors.New("listen: satellite name is required")
	case cfg.Sessions == nil:
		return nil, errors.New("listen: sessions are required")
	case cfg.Transcriber == nil:
		return nil, errors.New("listen: transcriber is required")
	case cfg.Blobs == nil:
		return nil, errors.New("listen: blobs are required")
	case cfg.Journal == nil:
		return nil, errors.New("listen: journal is required")
	}
	if cfg.Endpointer == nil {
		cfg.Endpointer = NewEnergy()
	}
	if cfg.WakeWindow <= 0 {
		cfg.WakeWindow = DefaultWakeWindow
	}
	if cfg.LeadIn <= 0 {
		cfg.LeadIn = DefaultLeadIn
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	heard := make(chan struct{})
	close(heard)
	l := &Listener{cfg: cfg, ctx: ctx, log: cfg.Log, done: make(chan struct{}), last: heard}
	go l.watch()
	return l, nil
}

// Done closes once the context has ended and every goroutine has exited.
func (l *Listener) Done() <-chan struct{} { return l.done }

// Session is the live session on this link, or nil.
func (l *Listener) Session() *session.Session {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.liveLocked()
	return l.sess
}

// watch ends everything when the link does. The session cannot outlive the
// device carrying it, and its reason says so.
func (l *Listener) watch() {
	<-l.ctx.Done()
	l.mu.Lock()
	l.closed = true
	l.abortLocked()
	sess := l.sess
	l.sess = nil
	l.mu.Unlock()
	l.lost(sess)
	// Safe against the WaitGroup rule: closed is set under the lock every
	// spawn checks, so no Add can follow this Wait.
	l.wg.Wait()
	close(l.done)
}

// lost closes a session whose device is gone.
func (l *Listener) lost(sess *session.Session) {
	if sess == nil {
		return
	}
	if err := sess.Close(context.Background(), "device_lost"); err != nil {
		l.warn("close session on device loss", err)
	}
}

// OnMic is the bridge read loop's call. It appends to the open utterance and
// asks the endpointer where it ends; everything slow is elsewhere.
func (l *Listener) OnMic(channel uint8, pcm []byte) error {
	// Speech recognition and the embedder both want the echo-cancelled
	// channel; the raw one is not kept here.
	if channel != bridge.ChannelAEC {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.muted {
		return nil
	}
	if !l.woken && !l.confirming && !l.liveLocked() {
		// No session and no wake: the mic streams anyway (SPEC §3.2), and
		// none of it is ours to keep.
		l.abortLocked()
		return nil
	}
	b := l.cfg.Endpointer.Feed(pcm)
	if l.cur == nil {
		if b != Start {
			l.leadLocked(pcm)
			l.awaitSpeechLocked(pcm)
			return nil
		}
		l.startLocked(pcm)
		return nil
	}
	if err := l.cur.write(pcm); err != nil {
		if !errors.Is(err, stt.ErrTooLong) {
			l.warn("write utterance audio", err)
			return nil
		}
		// Past the bound it is a stuck mic or the television (ADR-0024):
		// decode what is held, and the rest starts a new utterance.
		b = End
	}
	if b == End {
		l.endLocked()
	}
	return nil
}

// leadLocked keeps the last LeadIn bytes before speech starts.
func (l *Listener) leadLocked(pcm []byte) {
	l.lead = append(l.lead, pcm...)
	if extra := len(l.lead) - l.cfg.LeadIn; extra > 0 {
		l.lead = slices.Clone(l.lead[extra:])
	}
}

// awaitSpeechLocked buffers what follows a wake. The window expiring with no
// speech is a false accept (SPEC §9.3), journalled with that audio so the
// retraining corpus gets the negative.
func (l *Listener) awaitSpeechLocked(pcm []byte) {
	if !l.woken {
		return
	}
	l.wake = append(l.wake, pcm...)
	if len(l.wake) < l.cfg.WakeWindow {
		return
	}
	audio := l.wake
	l.wake, l.woken = nil, false
	l.cfg.Endpointer.Reset()
	l.spawnLocked(func() { l.rejectWake(audio, "no_speech") })
}

// startLocked opens an utterance on the chunk speech began in.
func (l *Listener) startLocked(pcm []byte) {
	ctx, cancel := context.WithCancel(l.ctx)
	su, err := stt.Open(ctx, l.cfg.Transcriber, l.cfg.STT)
	if err != nil {
		cancel()
		l.warn("open utterance", err)
		return
	}
	u := &utterance{
		id: rand.Text(), stt: su, cancel: cancel,
		first: l.woken, prev: l.last,
		ticket: make(chan struct{}), ended: make(chan struct{}), aborted: make(chan struct{}),
	}
	// The wake is spent on this utterance, whatever becomes of it. A wake that
	// a mute lands on is lost with it; the user muted the house.
	l.confirming = l.confirming || u.first
	l.woken, l.wake = false, nil
	l.last = u.ticket
	for _, chunk := range [][]byte{l.lead, pcm} {
		if len(chunk) == 0 {
			continue
		}
		if err := u.write(chunk); err != nil {
			l.warn("write utterance audio", err)
		}
	}
	l.lead = l.lead[:0]
	l.cur = u
	if !l.spawnLocked(func() { l.run(u) }) {
		cancel()
		close(u.ticket)
	}
}

// confirmed ends the confirming state, whichever way the first utterance went.
func (l *Listener) confirmed() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.confirming = false
}

// endLocked closes the open utterance naturally; its goroutine finishes it.
func (l *Listener) endLocked() {
	u := l.cur
	l.cur = nil
	l.lead = l.lead[:0]
	close(u.ended)
}

// abortLocked drops the open utterance without a transcript.
func (l *Listener) abortLocked() {
	l.lead = l.lead[:0]
	l.cfg.Endpointer.Reset()
	if l.cur == nil {
		return
	}
	u := l.cur
	l.cur = nil
	u.cancel()
	close(u.aborted)
}

// spawnLocked starts a goroutine the listener waits for. Refused once the
// context has ended, so nothing starts behind the shutdown.
func (l *Listener) spawnLocked(f func()) bool {
	if l.closed {
		return false
	}
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		f()
	}()
	return true
}

// liveLocked reports whether the session is still open, forgetting one that
// ended on its own: the model closed it, the backstop fired, or the person
// woke another satellite (SPEC §4.5).
func (l *Listener) liveLocked() bool {
	if l.sess == nil {
		return false
	}
	select {
	case <-l.sess.Done():
		l.sess = nil
		return false
	default:
		return true
	}
}

// OnWake arms the listener. The session opens only once the first utterance
// confirms the wake (SPEC §9.3): the wire carries no pre-roll, so there is
// nothing to judge before then, and the person the conversation is keyed on
// is not known before then either (SPEC §4.5).
func (l *Listener) OnWake(string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.muted || l.confirming || l.cur != nil || l.liveLocked() {
		return nil
	}
	l.woken, l.wake = true, l.wake[:0]
	return nil
}

// OnPlayed tracks the DAC's cumulative position, the number a candidate
// carries (SPEC §3.2.1). Monotonic for the same reason as the satellite's:
// a report that walks backwards is a desync, not a rewind.
func (l *Listener) OnPlayed(p bridge.Played) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if p.Frames > l.played {
		l.played = p.Frames
	}
	return nil
}

// OnMute is authoritative (CONTRIBUTING §7). Muted, audio is dropped on the
// floor, an open utterance ends with no transcript, a pending wake is spent,
// and nothing is buffered through to the other side.
func (l *Listener) OnMute(m bridge.Mute) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.muted = m.Hardware || m.Software
	if l.muted {
		l.abortLocked()
		l.woken, l.wake = false, nil
	}
	return nil
}

// utterance is one segment of speech: its audio, its STT adapter, and the
// channels OnMic signals its goroutine on.
type utterance struct {
	id     string
	stt    *stt.Utterance
	cancel context.CancelFunc

	// first marks the wake's first utterance, which confirms or rejects it.
	first bool

	prev    <-chan struct{}
	ticket  chan struct{}
	ended   chan struct{}
	aborted chan struct{}

	// Touched only by run.
	bargedIn   bool
	candidates int

	mu    sync.Mutex
	pcm   []byte
	sumsq float64
}

func (u *utterance) key() string { return "mic/" + u.id }

// write hands a chunk to STT and keeps it for the embedder and the blob.
// Kept only on success, so the audio matches what was decoded.
func (u *utterance) write(pcm []byte) error {
	if err := u.stt.Write(pcm); err != nil {
		return err
	}
	u.mu.Lock()
	u.pcm = append(u.pcm, pcm...)
	u.sumsq += sumSquares(pcm)
	u.mu.Unlock()
	return nil
}

// snapshot is the audio so far and its RMS as a fraction of full scale.
func (u *utterance) snapshot() ([]byte, float64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	pcm := slices.Clone(u.pcm)
	return pcm, rms(u.sumsq, len(u.pcm)/bytesPerFrame)
}

// run is the utterance's goroutine: candidates while it is open, then the
// turn once it ends. Its ticket closes on every exit, so the one behind it
// never waits on an utterance that was aborted.
func (l *Listener) run(u *utterance) {
	defer close(u.ticket)
	defer u.cancel()
	if u.first {
		defer l.confirmed()
	}
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-u.aborted:
			return
		case r := <-u.stt.Partials():
			l.candidate(u, r)
		case <-u.ended:
			l.complete(u)
			return
		}
	}
}

// candidate offers a partial to the gate while the session is speaking. The
// gate decides and journals; this only gathers what it asks for (SPEC §4.3).
func (l *Listener) candidate(u *utterance, r stt.Result) {
	if u.bargedIn {
		return
	}
	l.mu.Lock()
	sess, played := l.sess, l.played
	if !l.liveLocked() {
		sess = nil
	}
	l.mu.Unlock()
	if sess == nil || !slices.Contains(sess.Children(), "speaking") {
		return
	}

	pcm, energy := u.snapshot()
	out := l.resolve(pcm)
	u.candidates++
	ref, err := l.store(u.key()+"."+strconv.Itoa(u.candidates), pcm)
	if err != nil {
		l.warn("store candidate audio", err)
		return
	}
	ok, err := sess.BargeIn(l.ctx, session.Candidate{
		PositionMS: int(l.offset(played).Milliseconds()),
		AudioRef:   ref,
		SpeakerID:  out.PersonID,
		Energy:     energy,
		Partial:    r.Text,
	})
	if err != nil {
		l.warn("offer barge-in", err)
	}
	// One interruption per utterance: speech is already stopping, and a
	// second detection would journal a cut that did not happen.
	u.bargedIn = ok
}

// offset turns the DAC's cumulative frame count into how far into the speech
// being interrupted the cut lands: the quantity the pair is reproducible
// against (SPEC §8), and the one the satellite's frames_played already counts.
// Clamped, because the two origins come from separate locks and a base taken
// after this position would otherwise read as a negative offset.
func (l *Listener) offset(played uint64) time.Duration {
	if l.cfg.Playback != nil {
		base := l.cfg.Playback.SpeechBase()
		played -= min(played, base)
	}
	return bridge.Played{Frames: played}.Position(bridge.SampleRate)
}

// complete finishes an ended utterance: the final decode, the speaker, the
// blob, then -- in order behind the utterance before it -- the turn.
func (l *Listener) complete(u *utterance) {
	res, err := u.stt.Finish(l.ctx)
	if err != nil {
		l.warn("finish utterance", err)
		return
	}
	pcm, _ := u.snapshot()
	if res.Text == "" {
		// Silence is not a request; after a wake it is a false accept.
		if u.first {
			l.rejectWake(pcm, "no_speech")
		}
		return
	}
	out := l.resolve(pcm)
	if u.first && !l.confirm(pcm, out) {
		return
	}
	ref, err := l.store(u.key(), pcm)
	if err != nil {
		l.warn("store utterance audio", err)
		return
	}

	select {
	case <-u.prev:
	case <-l.ctx.Done():
		return
	}
	sess := l.Session()
	if sess == nil {
		// Ended between this utterance and its turn; what it said is lost,
		// which is what a session the model closed means.
		return
	}
	err = sess.Heard(l.ctx, session.Transcript{
		Text: res.Text, SpeakerID: out.PersonID, AudioRef: ref, Embedding: out.Embedding,
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		l.warn("hear utterance", err)
	}
}

// confirm is stages two and three of wake confirmation (SPEC §9.3) on the
// first utterance, and opens the session when they pass. Stage two has
// already passed: the utterance decoded to words.
//
// Stage one, re-scoring the pre-roll against the wake model at a higher
// threshold, is not run: bridge.TypeWake carries the word alone and no
// pre-roll, so there is nothing to score. When the firmware sends one, the
// rejection it produces is wake_rejected with reason low_confidence, and
// the score it passes becomes session.Wake.Confidence; until then neither is
// invented (ADR-0030).
func (l *Listener) confirm(pcm []byte, out identity.Outcome) bool {
	switch out.Reason {
	case identity.BelowThreshold, identity.Ambiguous:
		// Stage three. Nobody enrolled, or no resolver, cannot judge and does
		// not: the fresh install must answer someone (SPEC §5).
		l.rejectWake(pcm, "unknown_speaker")
		return false
	}
	sess, err := l.cfg.Sessions.Open(l.ctx, session.Wake{Satellite: l.cfg.Satellite, PersonID: out.PersonID})
	if err != nil {
		l.warn("open session", err)
		return false
	}
	l.mu.Lock()
	adopted := !l.closed
	if adopted {
		l.sess = sess
	}
	l.mu.Unlock()
	if !adopted {
		l.lost(sess)
	}
	return adopted
}

// rejectWake journals a failed confirmation to the device's log, with the
// audio: the hard negative is the point (SPEC §9.3).
func (l *Listener) rejectWake(pcm []byte, reason string) {
	ref, err := l.store("wake/"+rand.Text(), pcm)
	if err != nil {
		l.warn("store rejected wake audio", err)
		return
	}
	// WithoutCancel, as the session records: a link that just dropped still
	// gets its negative written.
	_, err = l.cfg.Journal.Append(context.WithoutCancel(l.ctx), DeviceConversation(l.cfg.Satellite), journal.Record{
		Kind: journal.KindWakeRejected, AudioRef: ref, Fields: map[string]string{"reason": reason},
	})
	if err != nil {
		l.warn("journal rejected wake", err)
	}
}

// resolve attributes audio. A resolver that fails yields a guest: a sidecar
// that is down must not stop the household talking to the house (SPEC §5).
func (l *Listener) resolve(pcm []byte) identity.Outcome {
	if l.cfg.Speakers == nil {
		return identity.Outcome{}
	}
	out, err := l.cfg.Speakers.Resolve(l.ctx, pcm)
	if err != nil {
		l.warn("resolve speaker", err)
		return identity.Outcome{}
	}
	return out
}

// store commits one blob and returns its reference.
func (l *Listener) store(key string, pcm []byte) (string, error) {
	w, err := l.cfg.Blobs.Create(l.ctx, key)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", key, err)
	}
	if _, err := w.Write(pcm); err != nil {
		_ = w.Abort()
		return "", fmt.Errorf("write %s: %w", key, err)
	}
	ref, err := w.Commit()
	if err != nil {
		return "", fmt.Errorf("commit %s: %w", key, err)
	}
	return ref, nil
}

func (l *Listener) warn(what string, err error) {
	l.log.Warn(what, "satellite", l.cfg.Satellite, "err", err)
}
