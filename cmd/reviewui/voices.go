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
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/teagan42/chorus/internal/bridge"
	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/reviewui/ui"
	"github.com/teagan42/chorus/internal/stt"
)

// voicesRoute is where the household's voiceprints are listed, enrolled and
// removed: the screen over identities.yaml that cmd/enroll is the terminal's
// way into (SPEC §5, ADR-0025).
const voicesRoute = "/voices"

// speakerIDURLEnv is where chorusd and cmd/enroll find the sidecar
// (.env.example); the same variable so one .env serves all three.
const speakerIDURLEnv = "SPEAKERID_URL"

// sttURLEnv is the ear chorusd uses, read here to put words beside a clip.
const sttURLEnv = "STT_URL"

// maxEnrollBytes bounds one enrollment's upload. A take is 32 kB a second
// at the satellite's format, so this is minutes of phrases, not a household's
// recordings sent by mistake.
const maxEnrollBytes = 32 << 20

// voiceID is what a person's id may be: it goes on every transcript and into
// the memory screen's paths, so it is one plain word.
var voiceID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

// clipsRoute re-lists one satellite's clips when the reviewer picks another.
const clipsRoute = voicesRoute + "/clips"

// speechless are the rejection reasons that mean no words were decoded from
// the clip, so it holds no voice to embed however long it is. Measured
// 2026-10-13: a room under the old speech gate came through as 20 to 30 s of
// amplified silence, which the trim keeps whole and the embedder turns into a
// vector near no voice at all -- four such clips clustered with each other at
// 0.78 and with their own household's speech at 0.05, so offering them as
// takes poisons a voiceprint (ADR-0082).
var speechless = map[string]bool{"no_speech": true, "transcription_failed": true}

// clipScan bounds how many of a satellite's newest recordings are read to
// fill the list. A day of brief wakes must not turn one page render into
// hundreds of blob reads.
const clipScan = 120

// clipLimit is how many of a satellite's newest clips the screen offers. A
// day of wakes is longer than anyone scrolls to enroll from.
const clipLimit = 12

// maxClipBytes bounds one clip read out of the store. An utterance is capped
// at 30 s, which is under a megabyte at the satellite's format.
const maxClipBytes = 4 << 20

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
	// words dials the ear, to say what a clip holds before it is ticked.
	// Nil lists the clips without words.
	words func() (stt.Transcriber, error)
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
	Clips    clipPicker
}

// clipPicker offers one satellite's own recordings as takes. A browser's
// microphone is not a satellite's, and a voiceprint from it matches the
// satellite less surely (ADR-0080); these are the audio chorusd already kept.
type clipPicker struct {
	// Satellites is every satellite with a log, for the dropdown.
	Satellites []string
	// Satellite is the one listed, which is the first unless asked otherwise.
	Satellite string
	Rows      []clipRow
	Empty     *ui.EmptyState
}

// clipRow is one of a satellite's recordings as the screen offers it. The
// reviewer listens before ticking it: that hearing is the attestation that
// the voice is the person's and not the television's.
type clipRow struct {
	Ref   string
	When  string
	What  string
	Audio string
	// Voiced is how much of the clip was spoken, so a reviewer can prefer a
	// longer one: every row listed is already long enough to embed.
	Voiced string
	// Closest is whose voice the household's own model thinks this is, and
	// how near. The reviewer's ear is still the attestation, but a clip of
	// somebody else is now visible without playing all twelve (ADR-0082).
	Closest string
	// Said is what the ear read in the clip, empty when no ear is configured
	// or it did not answer.
	Said string
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

func (s *server) enrollForm(ctx context.Context, id, name, satellite string) enrollForm {
	f := enrollForm{ID: id, Name: name, Min: identity.MinUtterances, Phrases: guidedPhrases}
	f.Clips = s.clipPicker(ctx, satellite)
	switch {
	case s.voices.identities == "":
		f.Off = &ui.Alert{Title: "Enrollment is off.", Body: "There is no household file to write: start the review UI with -identities.", Tone: ui.ToneMuted}
	case s.voices.embedder == nil:
		f.Off = &ui.Alert{Title: "Enrollment is off.", Body: "There is no speaker-ID sidecar to embed the phrases: set " + speakerIDURLEnv + " (see .env.example) or pass -speakerid-url, then restart the review UI.", Tone: ui.ToneMuted}
	}
	return f
}

// satellites is every satellite with a log, newest-spoken first as the
// journal orders its conversations.
func (s *server) satellites(ctx context.Context) ([]string, error) {
	convs, err := s.journal.Conversations(ctx)
	if err != nil {
		return nil, err
	}
	var sats []string
	for _, c := range convs {
		if sat, ok := strings.CutPrefix(c, devicePrefix); ok {
			sats = append(sats, sat)
		}
	}
	slices.Sort(sats)
	return sats, nil
}

// deviceClips is one satellite's newest recordings that can actually be
// takes, whatever the event kept them: a refused wake is as much that room's
// audio as a session's is. A clip with too little speech in it is left out
// rather than offered: the list is short and unpaged, so a row nobody can
// tick costs a row that someone could.
func (s *server) deviceClips(ctx context.Context, sat string) ([]clipRow, error) {
	events, err := s.journal.Events(ctx, devicePrefix+sat)
	if err != nil {
		return nil, err
	}
	loc := s.now().Location()
	rows := make([]clipRow, 0, clipLimit)
	// Newest first, reading each candidate's audio to measure it, until the
	// list is full or the scan has looked at as many as it will.
	scanned := 0
	for i := len(events) - 1; i >= 0 && len(rows) < clipLimit && scanned < clipScan; i-- {
		e := events[i]
		if e.AudioRef == "" || speechless[e.Fields["reason"]] {
			continue
		}
		scanned++
		voiced, ok := s.clipVoiced(ctx, e.AudioRef)
		if !ok {
			continue
		}
		rows = append(rows, clipRow{
			Ref:    e.AudioRef,
			When:   e.At.In(loc).Format("Mon 2 Jan 15:04:05"),
			What:   clipWhat(e),
			Audio:  audioHref(e.AudioRef),
			Voiced: fmt.Sprintf("%.1f s spoken", voiced),
		})
	}
	s.describeClips(ctx, rows)
	return rows, nil
}

// clipDescribeWait bounds the whole enrichment. Both sidecars are asked once
// per row and neither is required to list a clip, so a slow or absent one
// costs the detail and not the screen.
const clipDescribeWait = 20 * time.Second

// describeClips fills in who each clip sounds like and what was said, in
// parallel and best-effort. A row whose sidecar did not answer keeps its
// length and its log line, which is what the screen offered before.
func (s *server) describeClips(ctx context.Context, rows []clipRow) {
	if len(rows) == 0 {
		return
	}
	var emb identity.Embedder
	var ids *identity.Identities
	if s.voices.embedder != nil {
		if e, err := s.voices.embedder(); err == nil {
			if h, err := s.voices.household(); err == nil && len(h.People) > 0 {
				emb, ids = e, h
			}
		}
	}
	var ear stt.Transcriber
	if s.voices.words != nil {
		if w, err := s.voices.words(); err == nil {
			ear = w
		}
	}
	if emb == nil && ear == nil {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, clipDescribeWait)
	defer cancel()
	var wg sync.WaitGroup
	for i := range rows {
		if d, ok := s.describedClip(rows[i].Ref); ok {
			rows[i].Closest, rows[i].Said = d.closest, d.said
			continue
		}
		wg.Add(1)
		go func(r *clipRow) {
			defer wg.Done()
			pcm, err := s.clipPCM(ctx, r.Ref)
			if err != nil {
				return
			}
			// Both sidecars read the voiced span, not the whole clip: it is
			// what the embedder is given anyway, and a quarter of the audio
			// is a quarter of the ear's time.
			voiced := identity.TrimSilence(pcm)
			if emb != nil {
				if v, err := emb.Embed(ctx, voiced); err == nil {
					if who, score, err := ids.Closest(v); err == nil && who != "" {
						r.Closest = fmt.Sprintf("sounds like %s, %.2f", who, score)
						if score < identity.DefaultAccept {
							r.Closest += " — under the threshold, so a guest today"
						}
					}
				}
			}
			if ear != nil {
				if res, err := ear.Transcribe(ctx, voiced); err == nil {
					r.Said = res.Text
				}
			}
			s.describeClip(r.Ref, clipDesc{closest: r.Closest, said: r.Said})
		}(&rows[i])
	}
	wg.Wait()
}

// clipDesc is what the sidecars said about one clip. A blob is immutable and
// so is its description, so it is read once however often the picker renders
// -- which is every time the dropdown changes.
type clipDesc struct{ closest, said string }

func (s *server) describedClip(ref string) (clipDesc, bool) {
	s.described.mu.Lock()
	defer s.described.mu.Unlock()
	d, ok := s.described.by[ref]
	return d, ok
}

func (s *server) describeClip(ref string, d clipDesc) {
	s.described.mu.Lock()
	defer s.described.mu.Unlock()
	if s.described.by == nil {
		s.described.by = map[string]clipDesc{}
	}
	s.described.by[ref] = d
}

// clipVoiced is how many seconds of a clip were spoken, and whether that is
// enough to embed. A clip that will not read is not a take either.
func (s *server) clipVoiced(ctx context.Context, ref string) (float64, bool) {
	pcm, err := s.clipPCM(ctx, ref)
	if err != nil {
		return 0, false
	}
	samples := len(identity.TrimSilence(pcm)) / 2
	return float64(samples) / bridge.SampleRate, samples >= identity.MinVoicedSamples
}

// clipPCM reads one clip out of the store, bounded, in the satellite's own
// format.
func (s *server) clipPCM(ctx context.Context, ref string) ([]byte, error) {
	rc, err := s.blobs.Open(ctx, ref)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	pcm, err := io.ReadAll(io.LimitReader(rc, maxClipBytes))
	if err != nil {
		return nil, err
	}
	// Odd bytes are half a sample, which is not this format.
	return pcm[:len(pcm)-len(pcm)%2], nil
}

// clipWhat says what the log knows was heard, so a reviewer can skip a clip
// before playing it.
func clipWhat(e journal.Event) string {
	parts := []string{string(e.Kind)}
	for _, f := range []string{"text", "wake_word", "reason"} {
		if v := e.Fields[f]; v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " · ")
}

func audioHref(ref string) string { return "/audio?ref=" + url.QueryEscape(ref) }

// clipPicker lists one satellite's clips, defaulting to the first satellite.
func (s *server) clipPicker(ctx context.Context, sat string) clipPicker {
	p := clipPicker{Satellite: sat}
	sats, err := s.satellites(ctx)
	if err != nil || len(sats) == 0 {
		p.Empty = &ui.EmptyState{Title: "No satellite has a log here.", Body: "Enrolling from a satellite's own recordings needs chorusd to have kept some; until then, upload or record takes."}
		return p
	}
	p.Satellites = sats
	// An unknown name would name a log nobody has: only the dropdown's own.
	if !slices.Contains(sats, p.Satellite) {
		p.Satellite = sats[0]
	}
	rows, err := s.deviceClips(ctx, p.Satellite)
	if err != nil {
		p.Empty = &ui.EmptyState{Title: "That satellite's log would not read.", Body: err.Error()}
		return p
	}
	p.Rows = rows
	if len(rows) == 0 {
		p.Empty = &ui.EmptyState{
			Title: "This satellite has kept nothing long enough to embed.",
			Body: fmt.Sprintf("A take needs %.1f s of speech. %s has clips, or none at all, but none that long: say a whole sentence to it rather than a few words, and they will appear here.",
				float64(identity.MinVoicedSamples)/bridge.SampleRate, p.Satellite),
		}
	}
	return p
}

// voicesClips re-lists the picker when the reviewer chooses another satellite.
func (s *server) voicesClips(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.render(w, "enroll-clips", s.clipPicker(r.Context(), r.URL.Query().Get("satellite")))
}

// voicesPage lists who is enrolled and enrolls someone new.
func (s *server) voicesPage(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var said *ui.Alert
	if id := r.URL.Query().Get("enrolled"); id != "" {
		said = &ui.Alert{Title: "Enrolled " + id + ".", Body: restartChorusd, Tone: ui.TonePeople}
	}
	s.renderVoices(r.Context(), w, http.StatusOK, said, s.enrollForm(r.Context(), "", "", r.URL.Query().Get("satellite")))
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
	satellite := strings.TrimSpace(r.PostFormValue("satellite"))

	s.mu.Lock()
	defer s.mu.Unlock()
	refuse := func(status int, title, body string) {
		s.renderVoices(r.Context(), w, status, &ui.Alert{Title: title, Body: body, Tone: ui.ToneConv}, s.enrollForm(r.Context(), id, name, satellite))
	}
	if f := s.enrollForm(r.Context(), id, name, satellite); f.Off != nil {
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

	// A take is an uploaded file or one of the satellite's own clips, and the
	// two count together: three phrases is three phrases either way.
	files := r.MultipartForm.File["take"]
	clips := r.PostForm["clip"]
	if len(files)+len(clips) < identity.MinUtterances {
		refuse(http.StatusBadRequest, "Nobody was enrolled.", fmt.Sprintf("%s, want at least %d: one phrase's embedding is that phrase as much as the voice.",
			plural(len(files)+len(clips), "take"), identity.MinUtterances))
		return
	}
	takes := make([][]byte, 0, len(files)+len(clips))
	for _, fh := range files {
		pcm, err := readUpload(fh)
		if err != nil {
			refuse(http.StatusBadRequest, "Nobody was enrolled.", fmt.Sprintf("%s %v.", fh.Filename, err))
			return
		}
		takes = append(takes, pcm)
	}
	if len(clips) > 0 {
		// Only the listed satellite's own clips, so no other blob the store
		// holds -- the assistant's own speech among them -- becomes a voice.
		offered, err := s.deviceClips(r.Context(), satellite)
		if err != nil {
			refuse(http.StatusInternalServerError, "Nobody was enrolled.", "That satellite's log would not read: "+err.Error())
			return
		}
		for _, ref := range clips {
			pcm, err := s.readClip(r.Context(), offered, ref)
			if err != nil {
				refuse(http.StatusBadRequest, "Nobody was enrolled.", err.Error())
				return
			}
			takes = append(takes, pcm)
		}
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

// readClip is one of the satellite's own clips as a take: already the
// satellite's format, so it is trimmed to what was said rather than parsed.
// A clip the screen did not offer is refused, and so is one with too little
// voice in it to identify anyone.
func (s *server) readClip(ctx context.Context, offered []clipRow, ref string) ([]byte, error) {
	if !slices.ContainsFunc(offered, func(c clipRow) bool { return c.Ref == ref }) {
		return nil, fmt.Errorf("%s is not one of that satellite's clips", ref)
	}
	pcm, err := s.clipPCM(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("%s would not read: %w", ref, err)
	}
	voiced := identity.TrimSilence(pcm)
	if len(voiced)/2 < identity.MinVoicedSamples {
		return nil, fmt.Errorf("%s holds %.1f s of speech, and a take needs %d s: a shorter one embeds the room as much as the voice",
			ref, float64(len(voiced)/2)/bridge.SampleRate, identity.MinVoicedSamples/bridge.SampleRate)
	}
	return voiced, nil
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
