package ollama

import (
	"context"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/session"
)

// The model said goodbye and called end_session, and the session closing on
// it cancels the turn just as the stream says done. The model finished, so
// the turn still reports its end: the completion is the journal's, and
// replay cannot regenerate it (SPEC §8). Run many times: without the end
// sent on a context that cannot be cancelled, the cancel wins the race
// about half of them.
//
// verifies SPEC §8
func TestAFinishedTurnReportsItsEndThoughItWasCancelled(t *testing.T) {
	const stream = `{"model":"qwen3:14b","message":{"role":"assistant","content":""},"done":false}` + "\n" +
		`{"model":"qwen3:14b","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}` + "\n"
	for i := range 100 {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		out := make(chan session.Action)
		done := make(chan error, 1)
		go func() {
			done <- decoder{}.decode(ctx, strings.NewReader(stream), out)
			close(out)
		}()
		var ends []session.TurnEnd
		for a := range out {
			if e, ok := a.(session.TurnEnd); ok {
				ends = append(ends, e)
			}
		}
		<-done
		if len(ends) != 1 || ends[0].FinishReason != "stop" {
			t.Fatalf("run %d: ends = %+v, want the finished turn's one stop", i, ends)
		}
	}
}
