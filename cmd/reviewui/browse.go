package main

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/ui"
	"github.com/teagan42/chorus/internal/triage"
)

// devicePrefix marks a satellite's own log: wake rejections, which opened no
// conversation, and its room's presence (listen.DeviceConversation).
const devicePrefix = "device:"

// houseLog is the household's own log of timers, which belong to no
// conversation (journal.HouseTimers).
const houseLog = journal.HouseTimers

// session is one stretch of a conversation on one satellite.
type session struct {
	seq       uint64
	at        time.Time
	satellite string
	speaker   string
	signals   []triage.Signal
}

// convSummary is what Browse needs from one conversation's log.
type convSummary struct {
	id       string
	start    time.Time
	end      time.Time
	first    string
	speaker  string
	turns    int
	sessions []session
	signals  []triage.Signal

	// announced is a conversation an announcement opened, with no wake word;
	// forWhom is who asked for it, or set its timer.
	announced bool
	forWhom   string
}

// who is whom the conversation was with: whoever spoke first, or whom an
// announcement nobody answered was for.
func (c convSummary) who() string {
	if c.speaker == "" && c.forWhom != "" {
		return "for " + c.forWhom
	}
	return c.speaker
}

func summarize(id string, events []journal.Event, sigs []triage.Signal) convSummary {
	c := convSummary{id: id, signals: sigs}
	for _, e := range events {
		if c.start.IsZero() {
			c.start = e.At
		}
		// A summary is written after the close, as long after as the model
		// took; dropped audio is recorded a horizon later (ADR-0065).
		if e.Kind != journal.KindConversationSummarized && e.Kind != journal.KindAudioDropped {
			c.end = e.At
		}
		switch e.Kind {
		case journal.KindSessionOpened:
			c.sessions = append(c.sessions, session{
				seq: e.Seq, at: e.At, satellite: e.Fields["satellite"], speaker: e.Fields["speaker_id"],
			})
		case journal.KindUtteranceTranscribed:
			c.turns++
			if c.first == "" {
				c.first, c.speaker = e.Fields["text"], e.Fields["speaker_id"]
			} else if c.speaker == "" {
				c.speaker = e.Fields["speaker_id"]
			}
		case journal.KindAnnouncementMade:
			// What an announcement opened with is what it said, not an ask.
			if c.first == "" && c.announced {
				c.first, c.forWhom = e.Fields["text"], e.Fields["requested_by"]
			}
		}
		if e.Kind == journal.KindSessionOpened && e.Seq == 1 {
			c.announced = e.Fields["announced"] == "true"
		}
	}
	// A signal belongs to the session its turn ran under, which for a late
	// tool result is not the last one opened before it.
	for _, sig := range sigs {
		for i := len(c.sessions) - 1; i >= 0; i-- {
			if c.sessions[i].seq <= sig.Session {
				c.sessions[i].signals = append(c.sessions[i].signals, sig)
				break
			}
		}
	}
	return c
}

// topSignal is the session's most important signal: the barge-in first, since
// it is the training signal, then a failure, a repeated ask, a slow answer, a flip.
func topSignal(sigs []triage.Signal) triage.Kind {
	rank := map[triage.Kind]int{
		triage.KindBargeIn: 5, triage.KindFailure: 4, triage.KindRepeated: 3,
		triage.KindSlow: 2, triage.KindSpeakerFlip: 1,
	}
	best := triage.Kind("")
	for _, s := range sigs {
		if rank[s.Kind] > rank[best] {
			best = s.Kind
		}
	}
	return best
}

// sessionFlag is the session's colour on the lane, its top signal's.
func sessionFlag(sigs []triage.Signal) ui.Tone {
	tones := map[triage.Kind]ui.Tone{
		triage.KindBargeIn: ui.TonePeople, triage.KindFailure: ui.ToneHome,
		triage.KindRepeated: ui.ToneVoice, triage.KindSlow: ui.ToneVoice,
		triage.KindSpeakerFlip: ui.TonePeople,
	}
	return tones[topSignal(sigs)]
}

// plural counts a noun the way a reader expects: "1 turn", "2 turns".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func conversationHref(id string) string { return "/conversations/" + url.PathEscape(id) }

func hour(t time.Time) float64 {
	return float64(t.Hour()) + float64(t.Minute())/60 + float64(t.Second())/3600
}

// deviceLog is what Browse draws from a satellite's own log.
type deviceLog struct {
	rejects  []journal.Event
	presence []presenceMark
}

// deviceOf is a satellite's rejected wakes and its room's presence.
func deviceOf(events []journal.Event) *deviceLog {
	dev := &deviceLog{}
	for _, e := range events {
		switch e.Kind {
		case journal.KindWakeRejected:
			dev.rejects = append(dev.rejects, e)
		case journal.KindPresenceChanged:
			dev.presence = append(dev.presence, presenceMark{e.At, e.Fields["state"]})
		}
	}
	return dev
}

// presenceMark is one presence_changed: present, absent or unknown.
type presenceMark struct {
	at    time.Time
	state string
}

// presenceSpans are the hours of [from, to) someone was in the room, as
// decimal hours on the lane. A span opens on present and closes on anything
// else; one still open runs to now, or to the end of the day.
func presenceSpans(marks []presenceMark, from, to, now time.Time) [][2]float64 {
	var out [][2]float64
	var since time.Time
	span := func(a, b time.Time) {
		if a.Before(from) {
			a = from
		}
		if b.After(to) {
			b = to
		}
		if !a.Before(b) {
			return
		}
		end := 24.0
		if b.Before(to) {
			end = hour(b.In(from.Location()))
		}
		out = append(out, [2]float64{hour(a.In(from.Location())), end})
	}
	for _, m := range marks {
		switch {
		case m.state == "present" && since.IsZero():
			since = m.at
		case m.state != "present" && !since.IsZero():
			span(since, m.at)
			since = time.Time{}
		}
	}
	if !since.IsZero() {
		span(since, now)
	}
	return out
}

// household reads every log that grew since it was last read: conversations
// to summarize, device logs for their rejected wakes and presence, and the
// unreviewed count every header badges.
func (s *server) household(r *http.Request, u *unread) ([]convSummary, map[string]*deviceLog, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pairs, err := s.pairs(r.Context(), u)
	if err != nil {
		return nil, nil, 0, err
	}
	ids, err := s.journal.Conversations(r.Context())
	if err != nil {
		return nil, nil, 0, err
	}
	s.logs.retain(ids)
	var convs []convSummary
	devices := map[string]*deviceLog{}
	for _, id := range ids {
		d, err := s.logs.of(r.Context(), s.journal, id)
		if err != nil {
			u.skip(id, err)
			continue
		}
		if id == houseLog {
			continue
		}
		if sat, ok := strings.CutPrefix(id, devicePrefix); ok {
			devices[sat] = d.device
			continue
		}
		if d.triageErr != nil {
			u.skip(id, fmt.Errorf("triage: %w", d.triageErr))
			continue
		}
		convs = append(convs, d.summary)
	}
	return convs, devices, unreviewedCount(pairs), nil
}

func (s *server) browse(w http.ResponseWriter, r *http.Request) {
	var u unread
	convs, devices, unreviewed, err := s.household(r, &u)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	now := s.now()
	loc := now.Location()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	day := today
	if q := r.URL.Query().Get("day"); q != "" {
		d, err := time.ParseInLocation(time.DateOnly, q, loc)
		if err != nil {
			http.Error(w, "day: want YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		day = d
	}
	next := day.AddDate(0, 0, 1)
	on := func(t time.Time) bool { t = t.In(loc); return !t.Before(day) && t.Before(next) }

	lanes := map[string]*ui.DayLane{}
	lane := func(sat string) *ui.DayLane {
		if lanes[sat] == nil {
			lanes[sat] = &ui.DayLane{Name: sat}
		}
		return lanes[sat]
	}
	type move struct {
		at       time.Time
		from, to string
	}
	var moves []move
	list := ui.List{
		ID:      "conversations",
		Columns: [6]string{"Signal", "First ask · turns", "", "Who · where", "Length", "When"},
		Empty:   &ui.EmptyState{Title: "No conversations this day.", Body: "Pick another day, or talk to a satellite."},
	}
	slices.SortFunc(convs, func(a, b convSummary) int { return b.start.Compare(a.start) })
	for _, c := range convs {
		if !on(c.start) {
			continue
		}
		var rooms []string
		for i, ses := range c.sessions {
			if on(ses.at) {
				label := fmt.Sprintf("%s %s", ses.satellite, ses.at.In(loc).Format("15:04"))
				if kind := topSignal(ses.signals); kind != "" {
					label += " · " + signalTags[kind].Text
				}
				l := lane(ses.satellite)
				l.Sessions = append(l.Sessions, ui.Session{
					Hour: hour(ses.at.In(loc)), Flag: sessionFlag(ses.signals), Href: conversationHref(c.id), Label: label,
				})
			}
			if i > 0 && c.sessions[i-1].satellite != ses.satellite {
				moves = append(moves, move{ses.at, c.sessions[i-1].satellite, ses.satellite})
			}
			if len(rooms) == 0 || rooms[len(rooms)-1] != ses.satellite {
				rooms = append(rooms, ses.satellite)
			}
		}
		tag := ui.SigTag{Text: "conversation", Tone: ui.ToneConv}
		if c.announced {
			tag = ui.SigTag{Text: "announcement", Tone: ui.ToneVoice}
		}
		if len(c.signals) > 0 {
			tag = signalTags[c.signals[0].Kind]
		}
		detail := plural(c.turns, "turn")
		if c.announced && c.turns == 0 {
			detail = "no wake word"
		}
		if n := len(c.signals); n > 0 {
			detail += " · " + plural(n, "signal")
		}
		list.Rows = append(list.Rows, ui.ListRow{
			Href: conversationHref(c.id), Tag: tag, Title: c.first, Detail: detail,
			Who:    c.who() + " · " + strings.Join(rooms, " → "),
			Figure: c.end.Sub(c.start).Round(time.Second).String(),
			When:   c.start.In(loc).Format("15:04"),
		})
	}
	for sat, dev := range devices {
		devHref := conversationHref(devicePrefix + sat)
		for _, e := range dev.rejects {
			if on(e.At) {
				lane(sat).Rejects = append(lane(sat).Rejects, ui.Reject{
					Hour: hour(e.At.In(loc)), Href: fmt.Sprintf("%s#seq-%d", devHref, e.Seq), Label: e.Fields["reason"],
				})
			}
		}
		if spans := presenceSpans(dev.presence, day, next, now); len(spans) > 0 {
			lane(sat).Presence = spans
		}
		// The satellite's own log is one click from its name.
		if l, ok := lanes[sat]; ok {
			l.Href = devHref
		}
	}

	dl := ui.DayLanes{From: 0, To: 24, Legend: &ui.Legend{Items: []ui.LegendItem{
		{Label: "conversation", Shape: "block", Tone: ui.ToneConv},
		{Label: "barge-in or speaker flip", Shape: "flag", Tone: ui.TonePeople},
		{Label: "failure", Shape: "flag", Tone: ui.ToneHome},
		{Label: "wake rejected at stage two", Shape: "tick"},
	}}}
	names := make([]string, 0, len(lanes))
	occupied := false
	for n := range lanes {
		occupied = occupied || len(lanes[n].Presence) > 0
		names = append(names, n)
	}
	slices.Sort(names)
	if occupied {
		// Only a satellite with a radar has presence to show.
		dl.Legend.Items = append(dl.Legend.Items, ui.LegendItem{Label: "room occupied (mmWave)", Shape: "presence"})
	}
	index := map[string]int{}
	for i, n := range names {
		l := lanes[n]
		l.Sub = plural(len(l.Sessions), "session")
		index[n] = i
		dl.Lanes = append(dl.Lanes, *l)
	}
	for _, m := range moves {
		if on(m.at) {
			dl.Migrations = append(dl.Migrations, ui.Migration{
				Hour: hour(m.at.In(loc)), FromLane: index[m.from], ToLane: index[m.to], Label: "moved rooms · same conversation",
			})
		}
	}

	dayHref := func(d time.Time) string { return ui.Routes[ui.StepBrowse] + "?day=" + d.Format(time.DateOnly) }
	nav := []ui.Button{
		{Label: "‹", AriaLabel: "Previous day", Href: dayHref(day.AddDate(0, 0, -1))},
		{Label: "›", AriaLabel: "Next day", Href: dayHref(next), Disabled: !next.Before(today.AddDate(0, 0, 1))},
	}

	s.render(w, "page-browse", map[string]any{
		"Doc":    s.doc("Browse · " + day.Format("Monday 2 January")),
		"Header": ui.NewAppHeader(ui.StepBrowse, unreviewed, "chorus · journal"),
		"Head": ui.PageHead{
			Eyebrow: "01 · Browse", Title: "The household's day",
			Subtitle: "Every conversation, on the satellite it happened at. Flags come from Triage's signals.",
		},
		"DayLabel":  day.Format("Monday 2 January"),
		"Today":     day.Equal(today),
		"Nav":       nav,
		"Lanes":     dl,
		"ListTitle": plural(len(list.Rows), "conversation"),
		"List":      list,
		"Unread":    u.alert(),
	})
}
