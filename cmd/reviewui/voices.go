package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/reviewui/ui"
)

// voicesRoute is where the household's voiceprints are listed, enrolled and
// removed: the screen over identities.yaml that cmd/enroll is the terminal's
// way into (SPEC §5, ADR-0025).
const voicesRoute = "/voices"

// speakerIDURLEnv is where chorusd and cmd/enroll find the sidecar
// (.env.example); the same variable so one .env serves all three.
const speakerIDURLEnv = "SPEAKERID_URL"

// maxEnrollBytes bounds one enrollment's upload. A take is 32 kB a second
// at the satellite's format, so this is minutes of phrases, not a household's
// recordings sent by mistake.
const maxEnrollBytes = 32 << 20

// voiceID is what a person's id may be: it goes on every transcript and into
// the memory screen's paths, so it is one plain word.
var voiceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// guidedPhrases are offered for a person to read, one per take. Different
// words on purpose: the centroid is the mean that cancels the phrase out.
var guidedPhrases = []string{
	"Hey, what's the weather going to be like this afternoon?",
	"Add oat milk and two lemons to the shopping list.",
	"Set a timer for twelve minutes and remind me to check the oven.",
	"Play something quiet in the kitchen while I cook dinner.",
	"Is the front door locked, and did anyone leave the garage open?",
}

// voices is where enrollment reads and writes, beside the journal.
type voices struct {
	// identities is the household file chorusd reads at startup; empty on
	// an instance with no household, which the screen says.
	identities string
	// embedder dials the speaker-ID sidecar; nil when none is configured,
	// and the screen lists who is enrolled but enrolls nobody.
	embedder func() (identity.Embedder, error)
}

// voiceRow is one enrolled person as the screen lists them: never the
// vector, which is biometric and stays in the file.
type voiceRow struct {
	ID, Name   string
	Utterances int
	Remove     *ui.Confirm
}

// voicesList is the screen's list, which a removal swaps whole.
type voicesList struct {
	Rows  []voiceRow
	Model string
	Empty *ui.EmptyState
	Note  string
}

// enrollForm is the enrollment form and why it cannot be used, if it can't.
type enrollForm struct {
	ID, Name string
	Min      int
	Phrases  []string
	Off      *ui.Alert
}

func voiceRemoveHref(id string) string { return voicesRoute + "/" + url.PathEscape(id) + "/remove" }

// household reads identities.yaml; a missing file is nobody enrolled.
func (v voices) household() (*identity.Identities, error) {
	ids, err := identity.Load(v.identities)
	if errors.Is(err, fs.ErrNotExist) {
		return &identity.Identities{}, nil
	}
	return ids, err
}

func (s *server) voicesList() (voicesList, error) {
	var l voicesList
	if s.voices.identities == "" {
		l.Empty = &ui.EmptyState{Title: "This review UI has no household file.", Body: "It was started without -identities, so there is nobody to list and nowhere to enroll anyone."}
		return l, nil
	}
	ids, err := s.voices.household()
	if err != nil {
		return l, err
	}
	l.Model = ids.Model
	for _, p := range ids.People {
		row := voiceRow{ID: p.ID, Name: p.Name, Utterances: p.Utterances}
		if !s.readOnly {
			row.Remove = &ui.Confirm{
				ID: "remove-voice-" + p.ID, Label: "Remove…", Tone: ui.TonePeople,
				Title: "Remove " + p.ID + "'s voiceprint?",
				Body: "Once chorusd restarts, " + p.ID + " is a guest: person-scoped tools close to them and the barge-in gate stops knowing their voice. " +
					"Nothing keeps a copy of the voiceprint; enrolling again takes new phrases. Their memories and conversations stay.",
				Action:  voiceRemoveHref(p.ID),
				Confirm: "remove", Button: "Remove it", Target: "#voices", Swap: "outerHTML",
			}
		}
		l.Rows = append(l.Rows, row)
	}
	if len(l.Rows) == 0 {
		l.Empty = &ui.EmptyState{Title: "Nobody is enrolled.", Body: "Every voice is a guest until someone is: person-scoped tools stay closed and barge-in cannot tell the household from the television."}
	}
	if s.readOnly {
		l.Note = "Read-only: this review UI removes nobody."
	}
	return l, nil
}

func (s *server) enrollForm(id, name string) enrollForm {
	f := enrollForm{ID: id, Name: name, Min: identity.MinUtterances, Phrases: guidedPhrases}
	switch {
	case s.voices.identities == "":
		f.Off = &ui.Alert{Title: "Enrollment is off.", Body: "There is no household file to write: start the review UI with -identities.", Tone: ui.ToneMuted}
	case s.voices.embedder == nil:
		f.Off = &ui.Alert{Title: "Enrollment is off.", Body: "There is no speaker-ID sidecar to embed the phrases: set " + speakerIDURLEnv + " (see .env.example) or pass -speakerid-url, then restart the review UI.", Tone: ui.ToneMuted}
	}
	return f
}

// voicesPage lists who is enrolled and enrolls someone new.
func (s *server) voicesPage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var said *ui.Alert
	if id := r.URL.Query().Get("enrolled"); id != "" {
		said = &ui.Alert{Title: "Enrolled " + id + ".", Body: restartChorusd, Tone: ui.TonePeople}
	}
	s.renderVoices(r.Context(), w, http.StatusOK, said, s.enrollForm("", ""))
}

// restartChorusd is said after every change: chorusd reads the household at
// startup only, and a person enrolled while it runs is a guest until then.
const restartChorusd = "Restart chorusd to hear the change: it reads the household at startup only."

func (s *server) renderVoices(ctx context.Context, w http.ResponseWriter, status int, said *ui.Alert, form enrollForm) {
	list, err := s.voicesList()
	if err != nil {
		http.Error(w, "The household file would not read: "+err.Error(), http.StatusInternalServerError)
		return
	}
	pairs, err := s.pairs(ctx, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	s.render(w, "page-voices", map[string]any{
		"Doc":    s.doc("Voices"),
		"Header": ui.NewAppHeader("", unreviewedCount(pairs), "chorus · journal"),
		"Head": ui.PageHead{
			Eyebrow: "Voices", Title: "Who it knows by voice",
			Subtitle: "The household's enrolled voiceprints, from identities.yaml. A voice that matches nobody here is a guest (SPEC §5).",
			Actions:  []ui.Button{{Label: "Memory ›", Href: memoryRoute}},
		},
		"Said": said,
		"List": list,
		"Form": form,
	})
}

// enroll embeds a person's takes over the sidecar and saves their centroid.
// Every take is read and checked before the first is embedded, so a bad file
// costs no sidecar calls and a refused enrollment writes nothing.
func (s *server) enroll(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxEnrollBytes)
	if err := r.ParseMultipartForm(maxEnrollBytes); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, fmt.Sprintf("Nothing was enrolled: the takes come to more than %d MB. A phrase is a few seconds; send those.", maxEnrollBytes>>20), http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Nothing was enrolled: the form did not read: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()
	id, name := strings.TrimSpace(r.PostFormValue("id")), strings.TrimSpace(r.PostFormValue("name"))

	s.mu.Lock()
	defer s.mu.Unlock()
	refuse := func(status int, title, body string) {
		s.renderVoices(r.Context(), w, status, &ui.Alert{Title: title, Body: body, Tone: ui.ToneConv}, s.enrollForm(id, name))
	}
	if f := s.enrollForm(id, name); f.Off != nil {
		refuse(http.StatusServiceUnavailable, "Nobody was enrolled.", f.Off.Body)
		return
	}
	if !voiceID.MatchString(id) {
		refuse(http.StatusBadRequest, "Nobody was enrolled.", fmt.Sprintf("%q is not an id: it goes on every transcript, so it is one word of letters, digits, dots, dashes or underscores, at most 64.", id))
		return
	}
	ids, err := s.voices.household()
	if err != nil {
		refuse(http.StatusInternalServerError, "Nobody was enrolled.", "The household file would not read: "+err.Error())
		return
	}
	for _, p := range ids.People {
		if p.ID == id {
			refuse(http.StatusConflict, "Nobody was enrolled.", id+" is already enrolled. Remove them first to enroll them again from new phrases.")
			return
		}
	}

	files := r.MultipartForm.File["take"]
	if len(files) < identity.MinUtterances {
		refuse(http.StatusBadRequest, "Nobody was enrolled.", fmt.Sprintf("%s, want at least %d: one phrase's embedding is that phrase as much as the voice.", plural(len(files), "take"), identity.MinUtterances))
		return
	}
	takes := make([][]byte, 0, len(files))
	for _, fh := range files {
		pcm, err := readUpload(fh)
		if err != nil {
			refuse(http.StatusBadRequest, "Nobody was enrolled.", fmt.Sprintf("%s %v.", fh.Filename, err))
			return
		}
		takes = append(takes, pcm)
	}

	emb, err := s.voices.embedder()
	if err != nil {
		refuse(http.StatusServiceUnavailable, "Nobody was enrolled.", "The speaker-ID sidecar is not usable: "+err.Error())
		return
	}
	if _, err := identity.EnrollFile(r.Context(), s.voices.identities, emb, id, name, takes); err != nil {
		refuse(http.StatusBadGateway, "Nobody was enrolled.", "Nothing was written: "+err.Error())
		return
	}
	http.Redirect(w, r, voicesRoute+"?enrolled="+url.QueryEscape(id), http.StatusSeeOther)
}

// readUpload is one uploaded take's PCM, refused unless it is already in the
// satellite's own format.
func readUpload(fh *multipart.FileHeader) ([]byte, error) {
	f, err := fh.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return identity.ParseWAV(b)
}

// removeVoice drops one person's voiceprint and saves the rest atomically.
func (s *server) removeVoice(w http.ResponseWriter, r *http.Request) {
	if s.readOnly {
		http.Error(w, readOnlyRefusal, http.StatusForbidden)
		return
	}
	if !confirmed(w, r, "remove") {
		return
	}
	id := r.PathValue("id")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.voices.identities == "" {
		http.Error(w, "This review UI has no household file, so there is nobody to remove.", http.StatusNotFound)
		return
	}
	ids, err := s.voices.household()
	if err != nil {
		http.Error(w, "Nobody was removed: the household file would not read: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if !ids.Remove(id) {
		http.Error(w, fmt.Sprintf("%s is not enrolled, so there is nobody to remove. They may be removed already: reload the page.", id), http.StatusNotFound)
		return
	}
	if err := ids.Save(s.voices.identities); err != nil {
		http.Error(w, "Nobody was removed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, voicesRoute, http.StatusSeeOther)
		return
	}
	list, err := s.voicesList()
	if err != nil {
		http.Error(w, "Removed, but the household file would not read again: "+err.Error(), http.StatusInternalServerError)
		return
	}
	list.Note = strings.TrimSpace("Removed " + id + ". " + restartChorusd + " " + list.Note)
	s.render(w, "voices-list", list)
}
