package main

import (
	"cmp"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/ui"
)

// arbitrationRoute is the report on wakes several satellites heard: whether
// the loudest, which answered, was plausibly the nearest (ADR-0072).
const arbitrationRoute = "/arbitration"

// closeCall is a margin under which a wake was nearly another satellite's:
// channel 0's AGC narrows the gap between near and far (ADR-0072).
const closeCall = 3.0

// marginBuckets are where a pair's margins fall, in dB, closest first.
var marginBuckets = []struct {
	under  float64
	column string
}{
	{closeCall, "< 3 dB"},
	{6, "3–6 dB"},
	{10, "6–10 dB"},
	{0, "≥ 10 dB"},
}

// answeredWithin is how soon after a loser's record the winner's session
// opens: its first utterance confirmed, about a second (SPEC §4.5).
const answeredWithin = 5 * time.Second

// contest is one wake a satellite lost, as its own log recorded it.
type contest struct {
	loser, winner string
	seq           uint64
	at            time.Time
	// levels is both levels recorded and readable; a log from before they
	// were has none, and no margin.
	levels               bool
	energy, winnerEnergy float64
}

// contestOf reads an arbitrated wake_rejected from loser's log.
func contestOf(loser string, e journal.Event) contest {
	c := contest{loser: loser, winner: e.Fields["lost_to"], seq: e.Seq, at: e.At}
	var err1, err2 error
	c.energy, err1 = strconv.ParseFloat(e.Fields["energy_dbfs"], 64)
	c.winnerEnergy, err2 = strconv.ParseFloat(e.Fields["winner_energy_dbfs"], 64)
	c.levels = err1 == nil && err2 == nil
	return c
}

// margin is how much louder the winner was, in dB.
func (c contest) margin() float64 { return c.winnerEnergy - c.energy }

func (c contest) pair() string { return c.loser + " → " + c.winner }

// href is the lost wake's row in its satellite's log.
func (c contest) href() string {
	return fmt.Sprintf("%s#seq-%d", conversationHref(devicePrefix+c.loser), c.seq)
}

func db(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) + " dB" }

// median of sorted values, which must not be empty.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// pairStats is one loser → winner pair's contests, margins ascending.
type pairStats struct {
	name     string
	contests []contest
	margins  []float64
	closest  contest
}

// pairsOfContests groups contests by pair, closest median first; a pair
// with no levels sorts last.
func pairsOfContests(cs []contest) []pairStats {
	byName := map[string]*pairStats{}
	var out []*pairStats
	for _, c := range cs {
		p := byName[c.pair()]
		if p == nil {
			p = &pairStats{name: c.pair()}
			byName[c.pair()] = p
			out = append(out, p)
		}
		p.contests = append(p.contests, c)
		if c.levels {
			if len(p.margins) == 0 || c.margin() < p.closest.margin() {
				p.closest = c
			}
			p.margins = append(p.margins, c.margin())
		}
	}
	stats := make([]pairStats, 0, len(out))
	for _, p := range out {
		slices.Sort(p.margins)
		stats = append(stats, *p)
	}
	slices.SortFunc(stats, func(a, b pairStats) int {
		return cmp.Or(cmp.Compare(unleveled(a), unleveled(b)), closer(a, b), cmp.Compare(a.name, b.name))
	})
	return stats
}

// unleveled sorts a pair no level was recorded for after the rest.
func unleveled(p pairStats) int {
	if len(p.margins) == 0 {
		return 1
	}
	return 0
}

// closer orders two pairs by median margin, when both have one.
func closer(a, b pairStats) int {
	if len(a.margins) == 0 || len(b.margins) == 0 {
		return 0
	}
	return cmp.Compare(median(a.margins), median(b.margins))
}

// answered is the conversation the winner opened just after c, if any.
func answered(c contest, convs []convSummary) string {
	best, at := "", time.Time{}
	for _, conv := range convs {
		for _, ses := range conv.sessions {
			if ses.satellite != c.winner || !ses.at.After(c.at) || ses.at.Sub(c.at) > answeredWithin {
				continue
			}
			if best == "" || ses.at.Before(at) {
				best, at = conv.id, ses.at
			}
		}
	}
	return best
}

func (s *server) arbitration(w http.ResponseWriter, r *http.Request) {
	var u unread
	convs, devices, unreviewed, err := s.household(r, &u)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	loc := s.now().Location()
	var all []contest
	for sat, dev := range devices {
		for _, e := range dev.lost {
			all = append(all, contestOf(sat, e))
		}
	}
	slices.SortFunc(all, func(a, b contest) int { return cmp.Or(a.at.Compare(b.at), cmp.Compare(a.loser, b.loser)) })

	pairs := ui.DataTable{
		ID: "arbitration-pairs", Label: "By pair",
		Columns: []string{"Lost → answered by", "Contests", "Median margin"},
		Widths:  "minmax(170px, 1.4fr) 100px 150px repeat(4, 90px) minmax(140px, 1fr)",
	}
	for _, b := range marginBuckets {
		pairs.Columns = append(pairs.Columns, b.column)
	}
	pairs.Columns = append(pairs.Columns, "Closest")
	var margins []float64
	for _, p := range pairsOfContests(all) {
		margins = append(margins, p.margins...)
		row := ui.TableRow{Cells: []ui.Cell{{Text: p.name}, {Text: strconv.Itoa(len(p.contests)), Mono: true}}}
		if len(p.margins) == 0 {
			row.Cells = append(row.Cells, ui.Cell{Text: "—", Mono: true, Muted: true})
		} else {
			row.Cells = append(row.Cells, ui.Cell{Text: db(median(p.margins)), Mono: true})
		}
		counts := make([]int, len(marginBuckets))
		for _, m := range p.margins {
			counts[bucketOf(m)]++
		}
		for _, n := range counts {
			row.Cells = append(row.Cells, ui.Cell{Text: strconv.Itoa(n), Mono: true, Muted: n == 0})
		}
		if len(p.margins) == 0 {
			row.Cells = append(row.Cells, ui.Cell{Text: "levels not recorded", Muted: true})
		} else {
			row.Cells = append(row.Cells, ui.Cell{
				Text: db(p.closest.margin()) + " · " + p.closest.at.In(loc).Format("15:04"), Href: p.closest.href(),
			})
		}
		pairs.Rows = append(pairs.Rows, row)
	}

	closest := ui.DataTable{
		ID: "arbitration-closest", Label: "Closest calls · smallest margin first",
		Columns: []string{"When", "Lost → answered by", "Levels", "Margin", "Lost wake", "Answered in"},
		Widths:  "110px minmax(160px, 1fr) minmax(220px, 1.6fr) 90px minmax(170px, 1fr) minmax(170px, 1fr)",
	}
	var leveled []contest
	for _, c := range all {
		if c.levels {
			leveled = append(leveled, c)
		}
	}
	slices.SortStableFunc(leveled, func(a, b contest) int { return cmp.Compare(a.margin(), b.margin()) })
	within := 0
	for i, c := range leveled {
		if c.margin() < closeCall {
			within++
		}
		if i >= closestShown {
			continue
		}
		levels := fmt.Sprintf("%.1f against %.1f dBFS", c.energy, c.winnerEnergy)
		if c.margin() <= 0 {
			levels += " · no louder: a button press, or a tie the earlier wake took"
		}
		conv := ui.Cell{Text: "—", Muted: true}
		if id := answered(c, convs); id != "" {
			conv = ui.Cell{Text: id, Href: conversationHref(id)}
		}
		closest.Rows = append(closest.Rows, ui.TableRow{Cells: []ui.Cell{
			{Text: c.at.In(loc).Format("2 Jan 15:04"), Mono: true},
			{Text: c.pair()},
			{Text: levels, Mono: true},
			{Text: db(c.margin()), Mono: true},
			{Text: fmt.Sprintf("%s%s #%d", devicePrefix, c.loser, c.seq), Href: c.href()},
			conv,
		}})
	}

	med := "—"
	if len(margins) > 0 {
		slices.Sort(margins)
		med = db(median(margins))
	}
	var empty *ui.EmptyState
	if len(all) == 0 {
		empty = &ui.EmptyState{
			Title: "No wake was argued over.",
			Body:  "A wake two satellites hear at once lands here once one of them loses it, with both levels. A house of one satellite never has one.",
		}
	}
	s.render(w, "page-arbitration", map[string]any{
		"Doc":    s.doc("Arbitration"),
		"Header": ui.NewAppHeader("", unreviewed, "chorus · journal"),
		"Head": ui.PageHead{
			Eyebrow: "Arbitration", Title: "Is the loudest satellite the nearest?",
			Subtitle: "A wake several satellites heard was answered by the one whose processed channel was loudest; each other one logged what it lost by. " +
				"That channel is AGC'd, which narrows the gap between a near satellite and a far one, so a small margin is where to listen: play the lost wake beside the conversation it opened.",
			Stats: []ui.Metric{
				{Label: "Contests", Value: strconv.Itoa(len(all))},
				{Label: "Median margin", Value: med},
				{Label: "Within 3 dB", Value: strconv.Itoa(within)},
			},
			Actions: []ui.Button{{Label: "‹ The day", Href: ui.Routes[ui.StepBrowse]}},
		},
		"Empty":   empty,
		"Pairs":   pairs,
		"Closest": closest,
		"Unread":  u.alert(),
	})
}

// closestShown is how many of the closest calls the report lists.
const closestShown = 10

// bucketOf is which of marginBuckets m falls in.
func bucketOf(m float64) int {
	for i, b := range marginBuckets[:len(marginBuckets)-1] {
		if m < b.under {
			return i
		}
	}
	return len(marginBuckets) - 1
}
