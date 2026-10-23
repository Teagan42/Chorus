package listen

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/teagan42/chorus/internal/journal"
)

// Judge says how likely the turn so far is finished, from its audio. Smart
// Turn fills it (internal/provider/smartturn).
type Judge interface {
	Complete(ctx context.Context, pcm []byte) (Judgement, error)
}

// Judgement is the judge's answer about one pause.
type Judgement struct {
	// Probability is how likely the turn is over, from 0 to 1: Smart Turn's,
	// whatever else had a say.
	Probability float64

	// Dangling is a turn whose words end on one no command ends on. It is
	// held whatever the probability says (ADR-0042).
	Dangling bool
}

// DefaultSure is the probability at or above which a turn ends at the pause:
// 0.8. Below it, and at or above DefaultDoubt, the judge is unsure and the
// turn ends after Silence, as if nobody had judged it (ADR-0094).
const DefaultSure = 0.8

// DefaultDoubt is the probability under which a turn is held open to Hold:
// 0.3 (ADR-0094).
const DefaultDoubt = 0.3

// DefaultPause is the quiet after which the judge is asked: 200 ms, the
// pause Smart Turn is run on upstream.
const DefaultPause = 200 * bytesPerSecond / 1000

// DefaultHold is the longest a turn the judge called unfinished is held open
// through quiet: 2 s. Unmeasured; the Slow tab shows what it costs.
const DefaultHold = 2 * bytesPerSecond

// DefaultWindow is how much of the turn the judge is sent: the 8 s Smart Turn
// looks at.
const DefaultWindow = 8 * bytesPerSecond

// verdict is what the judge said about the current pause.
type verdict uint8

const (
	unasked verdict = iota
	pending
	finished
	unsure
	unfinished
	failed
)

// EndRule is what ended an utterance, so the journal can say which of the
// endpointer's rules the household waited on (ADR-0094).
type EndRule string

const (
	// EndSilence is trailing quiet with no verdict to go on: Energy's rule,
	// and Semantic's when the judge failed or did not answer in time.
	EndSilence EndRule = "silence"
	// EndFinished is a judge sure the turn was over, at the pause.
	EndFinished EndRule = "finished"
	// EndUnsure is a judge between its bands, so the turn ended after
	// Silence, as it would have with no judge.
	EndUnsure EndRule = "unsure"
	// EndHeld is a judge that doubted the turn was over, and a hold that ran
	// out without more speech.
	EndHeld EndRule = "held"
	// EndDangling is words that ended on one no command ends on, and a hold
	// that ran out without more speech (ADR-0042).
	EndDangling EndRule = "dangling"
)

// Ending is how an utterance ended: the rule, and the probability the judge
// gave the pause it ended on. Probability means nothing under EndSilence,
// which no verdict decided.
type Ending struct {
	Rule        EndRule
	Probability float64
}

// Ender is an Endpointer that can say how its last End was decided.
type Ender interface {
	Ending() Ending
}

// Semantic is SPEC §4.5's endpointer: energy says where speech is, and after
// a short pause the judge says how likely the words were a whole turn. A
// turn it is sure of ends then; one it doubts is held open to Hold; one it
// is unsure of, or has no answer for, ends where Energy would (ADR-0036,
// ADR-0094).
//
// Quiet is counted in audio, but the judge answers on the clock. Audio that
// arrives faster than it was spoken, the burst a satellite sends after its
// radio stalls, would otherwise reach Silence before a judge on time could
// answer. So a pending verdict holds the turn until the judge has had, by
// the clock, the time Silence would have given it, and never past Hold
// (ADR-0048).
type Semantic struct {
	// Threshold is the RMS fraction of full scale that counts as speech.
	Threshold float64

	// Pause, Silence, Hold, Window and LeadIn are in bytes. Silence ends a
	// turn the judge has not answered for, as Energy would. LeadIn is the
	// onset the threshold missed, kept as the listener keeps it for STT.
	Pause   int
	Silence int
	Hold    int
	Window  int
	LeadIn  int

	// Sure and Doubt band the judge's probability: at or above Sure the
	// turn ends at the pause, under Doubt it is held to Hold, and between
	// them it ends after Silence. Equal, they are a single cut.
	Sure  float64
	Doubt float64

	Judge Judge

	// Go runs an ask off the read loop, never before returning, and reports
	// whether it could. Nil takes the listener's goroutines.
	Go func(func()) bool

	// Log hears when the judge stops and starts answering, and each verdict
	// at debug level. Nil discards.
	Log *slog.Logger

	// Clock times the judge's answer. Nil takes the listener's; with neither,
	// the judge is timed by the audio alone, as if it came in real time.
	Clock journal.Clock

	ctx context.Context

	mu      sync.Mutex
	voiced  bool
	quiet   int
	trail   int
	audio   []byte
	said    verdict
	pause   uint64
	cancel  context.CancelFunc
	failing bool

	// heard is the verdict on the current pause, once one has landed.
	heard  Judgement
	ending Ending

	// due is when the judge's time for the current ask runs out.
	due time.Time
}

// NewSemantic returns the defaults around a judge.
func NewSemantic(j Judge) *Semantic {
	return &Semantic{
		Threshold: DefaultSpeechEnergy, Pause: DefaultPause, Silence: DefaultSilence,
		Hold: DefaultHold, Window: DefaultWindow, LeadIn: DefaultLeadIn,
		Sure: DefaultSure, Doubt: DefaultDoubt, Judge: j,
	}
}

// binder is an Endpointer whose model calls the listener owns. Feed runs
// under the listener's lock, so spawn may be its spawnLocked.
type binder interface {
	bind(ctx context.Context, spawn func(func()) bool, log *slog.Logger, clock journal.Clock)
}

var (
	_ binder = (*Semantic)(nil)
	_ Ender  = (*Semantic)(nil)
)

// bind hands the endpointer the listener's lifetime: asks end with the link,
// and the listener waits for them.
func (s *Semantic) bind(ctx context.Context, spawn func(func()) bool, log *slog.Logger, clock journal.Clock) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ctx = ctx
	if s.Go == nil {
		s.Go = spawn
	}
	if s.Log == nil {
		s.Log = log
	}
	if s.Clock == nil {
		s.Clock = clock
	}
}

func (s *Semantic) Feed(pcm []byte) Boundary {
	s.mu.Lock()
	defer s.mu.Unlock()
	loud := RMS(pcm) >= s.Threshold
	if !s.voiced {
		if !loud {
			s.audio = append(s.audio, pcm...)
			if extra := len(s.audio) - s.LeadIn; extra > 0 {
				s.audio = s.audio[extra:]
			}
			return Continue
		}
		s.voiced, s.quiet = true, 0
		s.keepLocked(pcm)
		return Start
	}
	s.keepLocked(pcm)
	if loud {
		if s.quiet > 0 {
			s.endPauseLocked()
		}
		s.quiet = 0
		return Continue
	}
	s.quiet += len(pcm)
	if s.said == unasked && s.quiet >= s.Pause {
		s.askLocked()
	}
	limit := s.Silence
	switch s.said {
	case finished:
		limit = 0
	case unfinished:
		limit = s.Hold
	case unsure:
		limit = s.Silence
	case pending:
		if s.Clock != nil && s.Clock.Now().Before(s.due) {
			limit = max(s.Silence, s.Hold)
		}
	}
	if s.quiet < limit {
		return Continue
	}
	s.trail = s.quiet
	s.ending = s.endingLocked()
	s.voiced, s.quiet, s.audio = false, 0, nil
	s.endPauseLocked()
	return End
}

// endingLocked names the rule the current pause is ending on.
func (s *Semantic) endingLocked() Ending {
	switch s.said {
	case finished:
		return Ending{Rule: EndFinished, Probability: s.heard.Probability}
	case unsure:
		return Ending{Rule: EndUnsure, Probability: s.heard.Probability}
	case unfinished:
		if s.heard.Dangling {
			return Ending{Rule: EndDangling, Probability: s.heard.Probability}
		}
		return Ending{Rule: EndHeld, Probability: s.heard.Probability}
	}
	return Ending{Rule: EndSilence}
}

func (s *Semantic) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.voiced, s.quiet, s.audio = false, 0, nil
	s.endPauseLocked()
}

// Paused is the pause the judge is asked about, from when it is asked.
func (s *Semantic) Paused() (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pause, s.voiced && s.quiet >= s.Pause
}

// Trailing is the quiet the last End waited through, in bytes.
func (s *Semantic) Trailing() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.trail
}

// Ending is how the last End was decided.
func (s *Semantic) Ending() Ending {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ending
}

// keepLocked holds the last Window bytes of the turn for the judge.
func (s *Semantic) keepLocked(pcm []byte) {
	s.audio = append(s.audio, pcm...)
	if extra := len(s.audio) - s.Window; extra > 0 {
		s.audio = s.audio[extra:]
	}
}

// endPauseLocked forgets the verdict on a pause that is over, and drops an
// ask still running on it: speech resumed, the turn ended, or it was reset.
func (s *Semantic) endPauseLocked() {
	s.pause++
	s.said = unasked
	s.heard = Judgement{}
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}

// askLocked sends the turn so far to the judge, off the read loop. A verdict
// lands only if its pause is still the current one.
func (s *Semantic) askLocked() {
	s.said = pending
	parent := s.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	if s.Clock != nil {
		left := time.Duration(max(s.Silence-s.quiet, 0)) * time.Second / bytesPerSecond
		s.due = s.Clock.Now().Add(left)
	}
	pause, audio := s.pause, bytes.Clone(s.audio)
	run := s.Go
	if run == nil {
		run = func(f func()) bool { go f(); return true }
	}
	if !run(func() { s.answer(ctx, pause, audio) }) {
		cancel()
		s.cancel = nil
		s.said = failed
	}
}

func (s *Semantic) answer(ctx context.Context, pause uint64, audio []byte) {
	v, err := s.Judge.Complete(ctx, audio)
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil || pause != s.pause {
		return
	}
	switch {
	case err != nil:
		s.said = failed
		if !s.failing {
			s.failing = true
			s.logger().Warn("semantic endpointing unavailable: turns end after trailing silence until the judge answers again", "err", err)
		}
		return
	case v.Dangling || v.Probability < s.Doubt:
		s.said = unfinished
	case v.Probability >= s.Sure:
		s.said = finished
	default:
		s.said = unsure
	}
	s.heard = v
	s.logger().Debug("semantic endpointing judged the pause", "probability", v.Probability, "dangling", v.Dangling)
	if s.failing {
		s.failing = false
		s.logger().Info("semantic endpointing answering again")
	}
}

func (s *Semantic) logger() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}
