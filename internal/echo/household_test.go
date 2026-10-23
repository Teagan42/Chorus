package echo_test

import (
	"sync"
	"testing"

	"github.com/teagan42/chorus/internal/echo"
	"github.com/teagan42/chorus/internal/echo/echotest"
	"github.com/teagan42/chorus/internal/reviewui/household"
)

// The household's whole day, judged: every answer the assistant gave coming
// back through the kitchen's microphone, and every person in the house --
// their asks, their barge-ins, the wakes -- talking a quarter second into
// every answer, once with nothing leaking behind them and once over the echo.
// These are ADR-0096's numbers; a change to the comparison that moves them
// has to move the ADR with them.
//
// verifies SPEC §4.3
func TestTheHouseholdsDayAgainstItsOwnAnswers(t *testing.T) {
	if testing.Short() {
		t.Skip("judges the household's whole day: seconds, or most of a minute under -race")
	}
	clips, err := household.Clips()
	if err != nil {
		t.Fatal(err)
	}
	var answers, people []string
	for _, c := range clips {
		switch {
		case c.Noise != "":
		case c.Who == "assistant":
			answers = append(answers, c.Ref)
		default:
			people = append(people, c.Ref)
		}
	}
	// tally is the day's count; each answer is judged on its own goroutine,
	// because the race detector makes a day of spectrograms slow.
	var mu sync.Mutex
	var echoes, caught, alone, aloneEcho, over, overEcho int
	add := func(n, hit *int, v echo.Verdict) {
		if !v.Judged {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		*n++
		if v.Echo {
			*hit++
		}
	}
	t.Run("day", func(t *testing.T) {
		for _, a := range answers {
			t.Run(a, func(t *testing.T) {
				t.Parallel()
				ref, played := playing(t, a)
				res := echotest.Kitchen.Residual(echotest.Samples(played))
				uplink := echotest.PCM(echotest.Kitchen.Uplink(res, nil, 0))
				for end := echotest.Rate / 2; end <= len(played)/2; end += echotest.Rate / 4 {
					add(&echoes, &caught, judge(ref, uplink, end))
				}
				const at = echotest.Rate / 4
				for _, p := range people {
					person := echotest.Samples(echotest.Clip(t, p))
					end := min(at+len(person), len(played)/2)
					nobody := make([]float64, len(res))
					add(&alone, &aloneEcho, judge(ref, echotest.PCM(echotest.Kitchen.Uplink(nobody, person, at)), end))
					add(&over, &overEcho, judge(ref, echotest.PCM(echotest.Kitchen.Uplink(res, person, at)), end))
				}
			})
		}
	})
	t.Logf("echo caught %d/%d; people alone called echo %d/%d; people over the echo %d/%d",
		caught, echoes, aloneEcho, alone, overEcho, over)
	if echoes < 100 || alone < 500 || over < 500 {
		t.Fatalf("judged %d echoes, %d people alone and %d over the echo: the day did not run", echoes, alone, over)
	}
	// At echo.DefaultThreshold.
	if rate := float64(caught) / float64(echoes); rate < 0.97 {
		t.Errorf("caught %.1f%% of the device's own voice, want at least 97%%", 100*rate)
	}
	if rate := float64(aloneEcho) / float64(alone); rate > 0.01 {
		t.Errorf("called %.1f%% of people speaking alone an echo, want at most 1%%", 100*rate)
	}
	if rate := float64(overEcho) / float64(over); rate > 0.02 {
		t.Errorf("called %.1f%% of people speaking over the echo an echo, want at most 2%%", 100*rate)
	}
}
