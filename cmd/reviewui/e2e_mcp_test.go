//go:build e2e

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/curation"
)

// Friday morning the reviewer opens Teagan's Porto question from Replay. The
// form starts from the registry's tools and the weather server's forecast
// the turn was offered, says so, and an unedited re-run of the same answer
// changes nothing. Labelled an exemplar, the turn downloads with the
// forecast among its tools.
//
// verifies SPEC §6, §9.1, §9.2
func TestE2EAnMCPTurnReRunsAndExportsWithTheHousesTools(t *testing.T) {
	s, decisions := newPortoServer(t, true, porto())
	p := open(t, s)

	p.visit("/replays/" + convPorto)
	p.waitText("form", "the registry's schema with the 1 MCP tool the turns were offered")
	var tools string
	p.eval(`document.querySelector("#replay-tools").value`, &tools)
	if !strings.Contains(tools, `"name": "`+portoForecast+`"`) {
		t.Errorf("the tool schema the form starts from has no %s", portoForecast)
	}
	p.shot("replay-mcp")

	p.click(`form button[type="submit"]`)
	p.waitText("#replay-result", "0 of 1")
	if strings.Contains(p.text("#replay-result"), "Tool schema diff") {
		t.Error("an unedited re-run shows a tool schema diff")
	}
	p.shot("replay-mcp-rerun")

	if err := decisions.PutAnnotation(context.Background(), curation.Annotation{
		ConversationID: convPorto, Seq: 2, Labels: []curation.Label{curation.LabelExemplar}, AnnotatedAt: time.Unix(1_760_039_000, 0).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	p.visit("/export")
	p.waitText(`a[href="/export/sft.jsonl"]`, "Download 1 exemplar (JSONL)")
	var sft string
	p.eval(`fetch("/export/sft.jsonl").then(r => r.text())`, &sft)
	if !strings.Contains(sft, `"name":"`+portoForecast+`"`) || strings.Contains(sft, "context_missing") {
		t.Errorf("the SFT download is %q, want the forecast offered and nothing missing", sft)
	}
}
