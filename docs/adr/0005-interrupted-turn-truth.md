# 0005. Record the interrupted turn truthfully, with an exact truncation point

- **Status:** accepted · unretrofittable (SPEC §15.1)
- **Source:** SPEC §4.4, §3.2.1, §9.1

On barge-in, what was actually spoken is kept and marked interrupted with its
truncation point; tool results that already arrived are kept; the unspoken
remainder is discarded but recorded as a distinct event type. The model sees "I
said this much, then was cut off" and may repeat itself in its own words.

The truncation point comes from the device's `add_audio_output_callback`, which
reports cumulative frames written to the DAC plus an `esp_timer` timestamp.
Error is bounded by the DAC FIFO and amp delay — sub-millisecond.

## Alternatives rejected

Estimating position from bytes sent. The best proxy on the stock path is
`bytes_sent_duration − [0.384, 0.512] s`, roughly ±128 ms, about 100× worse
(§3.2.1).

## Forecloses

Nothing downstream can recover this fidelity later, and the whole DPO corpus
rests on it: an interruption is a free preference pair only because the split
between heard and unheard text is exact (§9.1).

```go
import "github.com/teagan42/chorus/internal/journal"

// Truncation carries both halves of the split and the model versions in effect.
func truncationIsTrainingSignal() bool {
	m := journal.Meta[journal.KindSpeechTruncated]
	return m.TrainingSignal && m.RequiresVersions
}
```
