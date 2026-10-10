package main

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/teaganglenn/chorus/internal/hass"
	"github.com/teaganglenn/chorus/internal/identity"
	"github.com/teaganglenn/chorus/internal/journal"
	"github.com/teaganglenn/chorus/internal/listen"
	"github.com/teaganglenn/chorus/internal/provider/kokoro"
	"github.com/teaganglenn/chorus/internal/provider/ollama"
	"github.com/teaganglenn/chorus/internal/provider/smartturn"
	"github.com/teaganglenn/chorus/internal/provider/speaches"
	"github.com/teaganglenn/chorus/internal/provider/speakerid"
	"github.com/teaganglenn/chorus/internal/satellite"
	"github.com/teaganglenn/chorus/internal/session"
	"github.com/teaganglenn/chorus/internal/stt"
)

// Environment the daemon reads, as .env.example documents it. The journal
// and Home Assistant packages export their own names; these are the rest.
const (
	listenEnv       = "CHORUS_LISTEN"
	blobDirEnv      = "CHORUS_BLOB_DIR"
	ollamaURLEnv    = "OLLAMA_URL"
	ollamaModelEnv  = "OLLAMA_MODEL"
	kokoroURLEnv    = "KOKORO_URL"
	sttURLEnv       = "STT_URL"
	speakerIDURLEnv = "SPEAKERID_URL"
	smartTurnURLEnv = "SMARTTURN_URL"
)

// defaultListen is the port the firmware dials (esphome/packages/chorus-bridge.yaml).
const defaultListen = ":6055"

// Config is everything the daemon is told, from flags and the environment.
// Secrets stay in the environment and a gitignored .env (CONTRIBUTING §6).
type Config struct {
	Devices    string
	Identities string

	Listen       string
	DSN          string
	BlobDir      string
	OllamaURL    string
	OllamaModel  string
	KokoroURL    string
	STTURL       string
	SpeakerIDURL string
	SmartTurnURL string
	HassURL      string
	HassToken    string
}

// configFromEnv reads the environment through getenv, so a test never reads
// the real one. Defaults are the ones the rest of the repo already agrees on.
func configFromEnv(getenv func(string) string) Config {
	cfg := Config{
		Listen:       getenv(listenEnv),
		DSN:          getenv(journal.DSNEnv),
		BlobDir:      getenv(blobDirEnv),
		OllamaURL:    getenv(ollamaURLEnv),
		OllamaModel:  getenv(ollamaModelEnv),
		KokoroURL:    getenv(kokoroURLEnv),
		STTURL:       getenv(sttURLEnv),
		SpeakerIDURL: getenv(speakerIDURLEnv),
		SmartTurnURL: getenv(smartTurnURLEnv),
		HassURL:      getenv(hass.URLEnv),
		HassToken:    getenv(hass.TokenEnv),
	}
	if cfg.Listen == "" {
		cfg.Listen = defaultListen
	}
	if cfg.DSN == "" {
		cfg.DSN = journal.DefaultDSN
	}
	return cfg
}

// validate names every missing variable in one error, with what it is for,
// so one restart fixes them all rather than one per restart (SPEC §13).
func (c Config) validate() error {
	required := []struct{ name, value, purpose string }{
		{blobDirEnv, c.BlobDir, "directory for the audio the journal refers to"},
		{ollamaURLEnv, c.OllamaURL, "the turn engine's endpoint"},
		{ollamaModelEnv, c.OllamaModel, "the model the turn engine runs"},
		{kokoroURLEnv, c.KokoroURL, "the speech synthesis endpoint"},
		{sttURLEnv, c.STTURL, "the speech recognition endpoint"},
	}
	var missing []string
	for _, r := range required {
		if r.value == "" {
			missing = append(missing, fmt.Sprintf("  %-20s %s", r.name, r.purpose))
		}
	}
	// Half a Home Assistant configuration is a mistake, not a choice.
	switch {
	case c.HassURL != "" && c.HassToken == "":
		missing = append(missing, fmt.Sprintf("  %-20s the long-lived access token, since %s is set", hass.TokenEnv, hass.URLEnv))
	case c.HassToken != "" && c.HassURL == "":
		missing = append(missing, fmt.Sprintf("  %-20s the Home Assistant root, since %s is set", hass.URLEnv, hass.TokenEnv))
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("missing configuration (see .env.example):\n%s", strings.Join(missing, "\n"))
}

// providers is the model stack and the tools, resolved for one process and
// shared by every satellite's supervisor.
type providers struct {
	engine session.Engine
	// summarizer writes what each conversation was about when it ends: the
	// turn engine's own model, asked without tools (SPEC §5).
	summarizer session.Summarizer
	versions   journal.Versions
	synth      satellite.Synth
	stt        stt.Transcriber
	speakers   listen.Speakers
	judge      listen.Judge
	household  []string
	tools      map[string]session.Tool
}

// buildProviders constructs the providers without touching the network: a
// client is a URL and a config until its first request. ids is the enrolled
// household, or nil when nobody is.
//
// The optional sidecars degrade rather than fail, and never silently: a
// household without them is a house that still answers (SPEC §4.5, §5, §7).
func buildProviders(cfg Config, ids *identity.Identities, log *slog.Logger) (providers, error) {
	engine, err := ollama.New(ollama.Config{BaseURL: cfg.OllamaURL, Model: cfg.OllamaModel})
	if err != nil {
		return providers{}, err
	}
	synth, err := kokoro.New(kokoro.Config{BaseURL: cfg.KokoroURL})
	if err != nil {
		return providers{}, err
	}
	transcriber, err := speaches.New(speaches.Config{BaseURL: cfg.STTURL})
	if err != nil {
		return providers{}, err
	}
	// The engine knows the model, prompt and tool schema; which ear and voice
	// are in effect is a wiring fact only this function holds (SPEC §8).
	versions := engine.Versions()
	versions.STT, versions.TTS = transcriber.Version(), synth.Version()
	p := providers{
		engine: engine, summarizer: engine, versions: versions, synth: synth, stt: transcriber,
		tools: map[string]session.Tool{},
	}

	if cfg.SpeakerIDURL == "" {
		// Said here because the symptom is a house the television can
		// interrupt, and nothing else explains it (ADR-0031).
		log.Warn("speaker identification is off: "+speakerIDURLEnv+" is not set, so every speaker is a guest and barge-in gates on energy and partial length alone: any voice loud enough and long enough interrupts, the television included",
			"enrolled", enrolled(ids))
	} else {
		emb, err := speakerid.New(speakerid.Config{BaseURL: cfg.SpeakerIDURL})
		if err != nil {
			return providers{}, err
		}
		if ids == nil {
			// Nobody enrolled still embeds: SPEC §5 keeps the vector on every
			// transcript so voices can be clustered before anyone enrolls.
			ids = identity.New(emb)
		}
		resolver, err := identity.NewResolver(emb, ids, identity.Thresholds{})
		if err != nil {
			return providers{}, err
		}
		p.speakers, p.household = resolver, ids.Household()
		if len(p.household) == 0 {
			log.Warn("nobody is enrolled: every speaker is a guest until someone is (identities.yaml)")
		}
	}

	if cfg.SmartTurnURL == "" {
		log.Warn("semantic endpointing is off: " + smartTurnURLEnv + " is not set, so every turn ends after 800 ms of quiet: the household waits that long for every answer, and a longer pause mid-sentence cuts the speaker off")
	} else {
		judge, err := smartturn.New(smartturn.Config{BaseURL: cfg.SmartTurnURL})
		if err != nil {
			return providers{}, err
		}
		p.judge = judge
	}

	if cfg.HassURL == "" {
		log.Warn("home assistant is off: " + hass.URLEnv + " and " + hass.TokenEnv + " are not set, so the ha_* tools stay declared and answer not_implemented, which the model sees")
	} else {
		client, err := hass.New(hass.Config{BaseURL: cfg.HassURL, Token: cfg.HassToken})
		if err != nil {
			return providers{}, err
		}
		p.tools = hass.Tools(client)
	}
	return p, nil
}

func enrolled(ids *identity.Identities) int {
	if ids == nil {
		return 0
	}
	return len(ids.People)
}
