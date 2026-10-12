package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/erasure"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/retention"
	"github.com/teagan42/chorus/internal/reviewui/ui"
)

// Where the reviewer's deletions live (ADR-0070). Every destructive route is
// a POST behind the cross-origin check and the Host allow-list.
const (
	memoryRoute    = "/memory"
	deletionsRoute = "/deletions"
)

func deleteHref(id string) string { return conversationHref(id) + "/delete" }

func dropAudioHref(id string, seq uint64) string {
	return fmt.Sprintf("%s/events/%d/audio/drop", conversationHref(id), seq)
}

// readOnlyRefusal is what every destructive route says under -read-only.
const readOnlyRefusal = "This review UI was started read-only (-read-only), so it deletes nothing. Nothing was changed. Ask whoever runs it to start one without the flag."

// clockFunc is the server's clock as the journal takes one.
type clockFunc func() time.Time

func (f clockFunc) Now() time.Time { return f() }

// eraser is retention's deletion path over this server's stores, or why
// these stores cannot delete.
func (s *server) eraser() (*retention.Eraser, error) {
	store, ok := s.journal.(retention.Store)
	if !ok {
		return nil, errors.New("this journal cannot delete")
	}
	blobs, ok := s.blobs.(retention.Blobs)
	if !ok {
		return nil, errors.New("this blob store cannot remove audio")
	}
	cur, ok := s.decisions.(retention.Curation)
	if !ok {
		return nil, errors.New("this curation table cannot forget")
	}
	e := &retention.Eraser{
		Journal: journal.New(store, clockFunc(s.now), journal.Versions{}),
		Store:   store, Blobs: blobs, Curation: cur, Clock: clockFunc(s.now),
	}
	if s.memories != nil {
		e.Summaries = s.memories
	}
	return e, nil
}

// confirmed refuses a destructive post that did not come from its second
// press: the confirm panel's hidden field names the action.
func confirmed(w http.ResponseWriter, r *http.Request, want string) bool {
	if r.PostFormValue("confirm") == want {
		return true
	}
	http.Error(w, "Nothing was deleted: this asks for the second press in its panel, which says what goes and why it cannot come back. Open the panel and press its button.", http.StatusBadRequest)
	return false
}

// refusal is an eraser's refusal as the page says it, with its status.
func refusal(id string, err error) (string, int) {
	switch {
	case errors.Is(err, retention.ErrNoLog):
		return fmt.Sprintf("No conversation %q is in the journal this server reads, so there is nothing to delete. It may be gone already: reload the page.", id), http.StatusNotFound
	case errors.Is(err, retention.ErrNoEvent):
		return fmt.Sprintf("%s has no such event. Reload the page: the log may have moved on.", id), http.StatusNotFound
	case errors.Is(err, retention.ErrLive):
		return fmt.Sprintf("%s is a live session's: someone is talking in it now, and chorusd is writing to it. Nothing was deleted. Try again once the session closes.", id), http.StatusConflict
	case errors.Is(err, retention.ErrResumable):
		return fmt.Sprintf("%s ended moments ago, and its person may still walk back into it on another satellite. Nothing was deleted. Try again in a couple of minutes.", id), http.StatusConflict
	case errors.Is(err, retention.ErrLongLived):
		return fmt.Sprintf("%s is a satellite's or the house's own log, which chorusd writes all day. Nothing was deleted: retention trims it by its horizon instead.", id), http.StatusConflict
	case errors.Is(err, retention.ErrNoAudio):
		return "That event has no audio still kept: it was dropped or pruned already. Nothing else was changed.", http.StatusConflict
	}
	return fmt.Sprintf("Not everything of %s was deleted: %v. What is left is still listed; try again.", id, err), http.StatusInternalServerError
}

// redirect sends the browser to target: htmx by its header, a plain form
// post by a See Other.
func redirect(w http.ResponseWriter, r *http.Request, target string) {
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// record keeps the deletion's line in the trail. The deletion has already
// happened, so a failure here is said, not undone.
func (s *server) record(w http.ResponseWriter, r erasure.Record) bool {
	r.At = s.now()
	if _, err := s.erasures.Add(context.Background(), r); err != nil {
		http.Error(w, fmt.Sprintf("Deleted, but the record of it was not kept: %v. It is gone all the same; Deletions will not list it.", err), http.StatusInternalServerError)
		return false
	}
	return true
}

// deleteConversation removes a conversation whole, by retention's path, and
// sends the reviewer back to its day on Browse (SPEC §8).
func (s *server) deleteConversation(w http.ResponseWriter, r *http.Request) {
	if s.readOnly {
		http.Error(w, readOnlyRefusal, http.StatusForbidden)
		return
	}
	if !confirmed(w, r, "delete") {
		return
	}
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.eraser()
	if err != nil {
		http.Error(w, "Nothing was deleted: "+err.Error()+".", http.StatusInternalServerError)
		return
	}
	events, err := s.journal.Events(r.Context(), id)
	if err != nil {
		http.Error(w, "The journal would not read: "+err.Error(), http.StatusInternalServerError)
		return
	}
	erased, err := e.DeleteConversation(r.Context(), id)
	if err != nil {
		msg, code := refusal(id, err)
		http.Error(w, msg, code)
		return
	}
	s.logs.forget(id)
	counted := plural(erased.Events, "event") + ", " + plural(erased.Clips, "clip")
	if erased.Curated {
		counted += ", and a reviewer's curated work"
	}
	if !s.record(w, erasure.Record{Kind: erasure.KindConversation, ConversationID: id, Counted: counted}) {
		return
	}
	day := events[0].At.In(s.now().Location()).Format(time.DateOnly)
	redirect(w, r, ui.Routes[ui.StepBrowse]+"?day="+day+"&deleted="+url.QueryEscape(id))
}

// dropAudio removes one event's clips and records them as redacted in the
// log; the conversation stays, and its page shows the clip as gone.
func (s *server) dropAudio(w http.ResponseWriter, r *http.Request) {
	if s.readOnly {
		http.Error(w, readOnlyRefusal, http.StatusForbidden)
		return
	}
	if !confirmed(w, r, "drop") {
		return
	}
	id := r.PathValue("id")
	seq, err := strconv.ParseUint(r.PathValue("seq"), 10, 64)
	if err != nil {
		http.Error(w, fmt.Sprintf("%q is not an event of %s.", r.PathValue("seq"), id), http.StatusNotFound)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, err := s.eraser()
	if err != nil {
		http.Error(w, "Nothing was dropped: "+err.Error()+".", http.StatusInternalServerError)
		return
	}
	refs, err := e.DropAudio(r.Context(), id, seq)
	if err != nil {
		msg, code := refusal(id, err)
		http.Error(w, msg, code)
		return
	}
	counted := fmt.Sprintf("%s of #%d", plural(len(refs), "clip"), seq)
	if !s.record(w, erasure.Record{Kind: erasure.KindAudio, ConversationID: id, Subject: strings.Join(refs, " "), Counted: counted}) {
		return
	}
	if r.Header.Get("HX-Request") == "true" {
		// The rows below gain the record, so the whole page is read again.
		w.Header().Set("HX-Refresh", "true")
		return
	}
	http.Redirect(w, r, fmt.Sprintf("%s#seq-%d", conversationHref(id), seq), http.StatusSeeOther)
}

// deletable says whether the conversation page offers deletion, and if not
// why, which the page says in place of the controls.
func (s *server) deletable(id string, events []journal.Event) (bool, string) {
	if s.readOnly {
		return false, "Read-only: this review UI deletes nothing."
	}
	e, err := s.eraser()
	if err != nil {
		return false, ""
	}
	switch err := e.Refuses(id, events); {
	case errors.Is(err, retention.ErrLongLived):
		return false, ""
	case err != nil:
		msg, _ := refusal(id, err)
		return false, msg
	}
	return true, ""
}

// droppable says whether the page offers to drop its clips: a deletable
// conversation's, and a satellite's own log, which is never deleted whole
// but whose clips a reviewer may take out (ADR-0076).
func (s *server) droppable(id string, events []journal.Event) bool {
	if s.readOnly {
		return false
	}
	e, err := s.eraser()
	return err == nil && e.RefusesDrop(id, events) == nil
}

// deleteConfirm is the conversation page's deletion, behind its second press.
func deleteConfirm(id string, events []journal.Event) *ui.Confirm {
	clips := map[string]bool{}
	gone := goneClips(events)
	for _, e := range events {
		for _, ref := range []string{e.AudioRef, e.Fields["second_audio_ref"]} {
			if _, dropped := gone[ref]; ref != "" && !dropped {
				clips[ref] = true
			}
		}
	}
	return &ui.Confirm{
		ID: "delete-conversation", Label: "Delete this conversation…", Tone: ui.ToneHome,
		Title: "Delete " + id + " for good?",
		Body: fmt.Sprintf("Its %s, its %s, every verdict, label and re-run on it, and the summaries kept of it go now. "+
			"Nothing keeps a copy, so it cannot be restored; Deletions keeps only a line saying it went. "+
			"Facts someone asked to be remembered in it stay until they are forgotten on the memory screen.",
			plural(len(events), "event"), plural(len(clips), "clip")),
		Action: deleteHref(id), Confirm: "delete", Button: "Delete for good",
	}
}

// dropConfirm is one row's audio drop, behind its second press.
func dropConfirm(id string, seq uint64, second bool) *ui.Confirm {
	what := "this clip"
	if second {
		what = "this clip and its second channel"
	}
	stays := "The words stay; the recording cannot be restored."
	if strings.HasPrefix(id, devicePrefix) {
		stays = "Its line in the log stays, with any verdict on it; the recording cannot be restored."
	}
	return &ui.Confirm{
		ID: fmt.Sprintf("drop-%d", seq), Label: "Drop audio…", Tone: ui.ToneHome,
		Title:  fmt.Sprintf("Drop the audio of #%d?", seq),
		Body:   "Removes " + what + " for good, and records in the log that a reviewer dropped it. " + stays,
		Action: dropAudioHref(id, seq), Confirm: "drop", Button: "Drop for good",
	}
}

// curatedNote is a log a reviewer kept work of, said on its delete panel.
func curatedNote(ctx context.Context, d curation.Store, id string) string {
	if kept, err := curation.Curated(ctx, d, id); err == nil && kept {
		return " A reviewer kept work of this conversation for the dataset, and it goes too."
	}
	return ""
}
