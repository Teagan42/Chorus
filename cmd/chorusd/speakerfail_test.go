package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/ops"
)

// The household's speaker-ID sidecar was rebuilt on ECAPA-TDNN. It answers
// /health, so a probe that only asked whether it was up called it fine,
// and every embed is refused by the client. Now /readyz names it down with
// both models from the first round, the log says so before anyone speaks,
// and Alan's wake, which fails closed as it must, is recorded as
// speaker_id_failed rather than as a guest's (SPEC §9.3, §13, ADR-0097).
//
// verifies SPEC §9.3, §13
func TestASpeakerSidecarOnAnotherModelIsDownAndItsWakeSaysSo(t *testing.T) {
	answers := map[string]string{
		"speakerid:8890/health":       `{"status":"ok","model":"ecapa_tdnn","dim":192}`,
		"smartturn:8891/health":       `{"status":"ok","model":"smart-turn-v3.2-cpu"}`,
		"kokoro:8880/v1/audio/voices": `{"voices":["af_heart","am_michael"]}`,
	}
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(answers[r.URL.Host+r.URL.Path])), Request: r}, nil
	})}
	checks := opsChecks(configFromEnv(lookup(complete())), client, func(context.Context) error { return nil })

	r := newRig(t, inventory(), func(d *deps) { d.Ops = &opsDeps{Checks: checks} })
	r.emb.mu.Lock()
	r.emb.down = errors.New(`speakerid embed: sidecar serves "ecapa_tdnn", client is built for "nemo_en_titanet_large"`)
	r.emb.mu.Unlock()

	var speaker ops.Dependency
	await(t, "the speaker sidecar judged in /readyz", func() bool {
		for _, d := range r.readyz(t).Dependencies {
			if d.Name == "speaker_id" {
				speaker = d
			}
		}
		return speaker.State != ops.Unknown && speaker.State != ""
	})
	if speaker.State != ops.Down || !strings.Contains(speaker.Error, `"ecapa_tdnn", client is built for "nemo_en_titanet_large"`) {
		t.Errorf("speaker_id = %+v, want down naming both models", speaker)
	}
	r.logs.await(t, "dependency down")
	if !r.logs.contains("ecapa_tdnn") {
		t.Errorf("the startup log does not name the model:\n%s", r.logs)
	}

	dev := r.join(t, kitchenIP)
	dev.SendWake(t, "hey_eddie")
	r.utter(t, dev, r.line("is the back door locked", alan))

	rej := r.store.awaitKind(t, journal.KindWakeRejected, 1)
	if rej.Fields["reason"] != "speaker_id_failed" {
		t.Errorf("wake_rejected = %v, want speaker_id_failed", rej.Fields)
	}
	if n := len(r.store.ofKind(journal.KindSessionOpened)); n != 0 {
		t.Errorf("%d sessions opened on a voice nothing could attribute", n)
	}
	r.logs.await(t, "resolve speaker")
}
