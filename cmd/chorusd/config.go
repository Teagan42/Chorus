package main

import (
	"cmp"
	"fmt"
	"log/slog"
	"maps"
	"strconv"
	"strings"

	"github.com/teagan42/chorus/internal/hass"
	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/listen"
	"github.com/teagan42/chorus/internal/memory"
	"github.com/teagan42/chorus/internal/provider/kokoro"
	"github.com/teagan42/chorus/internal/provider/mcp"
	"github.com/teagan42/chorus/internal/provider/ollama"
	"github.com/teagan42/chorus/internal/provider/openai"
	"github.com/teagan42/chorus/internal/provider/smartturn"
	"github.com/teagan42/chorus/internal/provider/speaches"
	"github.com/teagan42/chorus/internal/provider/speakerid"
	"github.com/teagan42/chorus/internal/provider/vllm"
	"github.com/teagan42/chorus/internal/provider/wyoming"
	"github.com/teagan42/chorus/internal/registry"
	"github.com/teagan42/chorus/internal/satellite"
	"github.com/teagan42/chorus/internal/session"
	"github.com/teagan42/chorus/internal/stt"
)

// Environment the daemon reads, as .env.example documents it. The journal
// and Home Assistant packages export their own names; these are the rest.
const (
	listenEnv       = "CHORUS_LISTEN"
	blobDirEnv      = "CHORUS_BLOB_DIR"
	ollamaURLEnv    = "OLLAMA_URL"
	ollamaModelEnv  = "OLLAMA_MODEL"
	ollamaEmbedEnv  = "OLLAMA_EMBED_MODEL"
	kokoroURLEnv    = "KOKORO_URL"
	sttURLEnv       = "STT_URL"
	speakerIDURLEnv = "SPEAKERID_URL"
	smartTurnURLEnv = "SMARTTURN_URL"

	// The turn engine's provider, and vLLM's endpoint when it is the one
	// (ADR-0077). Ollama's are above: it stays the default, and keeps
	// serving the embedding model either way.
	llmProviderEnv  = "CHORUS_LLM_PROVIDER"
	vllmURLEnv      = "VLLM_URL"
	vllmModelEnv    = "VLLM_MODEL"
	vllmKeyEnv      = "VLLM_API_KEY"
	vllmThinkingEnv = "VLLM_THINKING"

	// speakerAcceptEnv is the cosine at or above which a voice is the
	// enrolled person it is nearest, overriding identity.DefaultAccept.
	// A front end that rides its own gain compresses the scores: measured
	// 2026-10-13, a household's own short commands through a Voice PE's
	// XMOS scored 0.45 to 0.79 against their own voiceprint while a second
	// enrolled voice never passed 0.43 against it, so who it was stayed
	// clear while the absolute score fell under the 0.70 default (ADR-0084).
	speakerAcceptEnv = "CHORUS_SPEAKER_ACCEPT"

	// The bands Smart Turn's probability is read in, overriding
	// listen.DefaultSure and listen.DefaultDoubt: at or above sure a turn
	// ends at the pause, under doubt it is held, and between them it ends
	// after Energy's silence. Each utterance journals the probability it
	// ended on, which is what a household tunes these from (ADR-0094).
	smartTurnSureEnv  = "CHORUS_SMARTTURN_SURE"
	smartTurnDoubtEnv = "CHORUS_SMARTTURN_DOUBT"

	// confirmEnv turns the person's yes off for calls that would be held.
	// "on" or unset holds them; "off" runs them as written (ADR-0091).
	confirmEnv = "CHORUS_CONFIRM"

	// speculateEnv answers a turn in its pause, kept only if nothing
	// changed by its end. "off" or unset waits for the end (ADR-0095).
	speculateEnv = "CHORUS_SPECULATE"

	// The voice's and the ear's providers, and the endpoints of the ones
	// that are not the defaults (ADR-0081). Kokoro's and speaches' are
	// above.
	ttsProviderEnv     = "CHORUS_TTS_PROVIDER"
	sttProviderEnv     = "CHORUS_STT_PROVIDER"
	openaiTTSURLEnv    = "OPENAI_TTS_URL"
	openaiTTSKeyEnv    = "OPENAI_TTS_API_KEY"
	openaiTTSModelEnv  = "OPENAI_TTS_MODEL"
	openaiTTSVoiceEnv  = "OPENAI_TTS_VOICE"
	openaiSTTURLEnv    = "OPENAI_STT_URL"
	openaiSTTKeyEnv    = "OPENAI_STT_API_KEY"
	openaiSTTModelEnv  = "OPENAI_STT_MODEL"
	wyomingTTSAddrEnv  = "WYOMING_TTS_ADDR"
	wyomingTTSVoiceEnv = "WYOMING_TTS_VOICE"
	wyomingSTTAddrEnv  = "WYOMING_STT_ADDR"
	wyomingSTTModelEnv = "WYOMING_STT_MODEL"
	sttLanguageEnv     = "STT_LANGUAGE"
)

// The turn engines CHORUS_LLM_PROVIDER chooses between.
const (
	providerOllama = "ollama"
	providerVLLM   = "vllm"
)

// The voices CHORUS_TTS_PROVIDER and the ears CHORUS_STT_PROVIDER choose
// between. OpenAI's audio API and Wyoming serve both.
const (
	providerKokoro   = "kokoro"
	providerSpeaches = "speaches"
	providerOpenAI   = "openai"
	providerWyoming  = "wyoming"
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
	EmbedModel   string
	KokoroURL    string
	STTURL       string
	SpeakerIDURL string
	SmartTurnURL string
	HassURL      string
	HassToken    string

	// LLMProvider is the turn engine, ollama or vllm (ADR-0077).
	LLMProvider string
	VLLMURL     string
	VLLMModel   string
	VLLMAPIKey  string
	// VLLMThinking is Qwen3's thinking switch as written; vllmThinking
	// reads it.
	VLLMThinking string

	// SpeakerAccept is the accept threshold as written; speakerAccept parses it.
	SpeakerAccept string

	// Confirm is CHORUS_CONFIRM as written; skipConfirmation parses it.
	Confirm string

	// Speculate is CHORUS_SPECULATE as written; speculate parses it.
	Speculate string

	// SmartTurnSure and SmartTurnDoubt are the bands as written; turnBands
	// parses them.
	SmartTurnSure  string
	SmartTurnDoubt string

	// TTSProvider is the voice: kokoro, openai or wyoming (ADR-0081).
	TTSProvider     string
	OpenAITTSURL    string
	OpenAITTSKey    string
	OpenAITTSModel  string
	OpenAITTSVoice  string
	WyomingTTSAddr  string
	WyomingTTSVoice string

	// STTProvider is the ear: speaches, openai or wyoming (ADR-0081).
	STTProvider     string
	OpenAISTTURL    string
	OpenAISTTKey    string
	OpenAISTTModel  string
	WyomingSTTAddr  string
	WyomingSTTModel string
	// STTLanguage is sent to the ear when set; speaches' Parakeet ignores
	// it, Whisper honours it.
	STTLanguage string

	// BlobMinFreeMiB is the disk guard's floor as written; blobFloor reads it.
	BlobMinFreeMiB string

	// OpsListen is where health, readiness and metrics are served, or "off".
	OpsListen string

	// WakeArbitrationMS is the arbitration window as written; wakeArbitration
	// reads it.
	WakeArbitrationMS string

	// MCPConfig is the household's MCP servers' file. Empty is none
	// (ADR-0075).
	MCPConfig string
}

// configFromEnv reads the environment through getenv, so a test never reads
// the real one. Defaults are the ones the rest of the repo already agrees on.
func configFromEnv(getenv func(string) string) Config {
	cfg := Config{
		Listen:        getenv(listenEnv),
		DSN:           getenv(journal.DSNEnv),
		BlobDir:       getenv(blobDirEnv),
		OllamaURL:     getenv(ollamaURLEnv),
		OllamaModel:   getenv(ollamaModelEnv),
		EmbedModel:    getenv(ollamaEmbedEnv),
		LLMProvider:   getenv(llmProviderEnv),
		VLLMURL:       getenv(vllmURLEnv),
		VLLMModel:     getenv(vllmModelEnv),
		VLLMAPIKey:    getenv(vllmKeyEnv),
		VLLMThinking:  getenv(vllmThinkingEnv),
		SpeakerAccept: getenv(speakerAcceptEnv),
		Confirm:       getenv(confirmEnv),
		Speculate:     getenv(speculateEnv),

		SmartTurnSure:  getenv(smartTurnSureEnv),
		SmartTurnDoubt: getenv(smartTurnDoubtEnv),
		KokoroURL:      getenv(kokoroURLEnv),
		STTURL:         getenv(sttURLEnv),

		TTSProvider:     getenv(ttsProviderEnv),
		OpenAITTSURL:    getenv(openaiTTSURLEnv),
		OpenAITTSKey:    getenv(openaiTTSKeyEnv),
		OpenAITTSModel:  getenv(openaiTTSModelEnv),
		OpenAITTSVoice:  getenv(openaiTTSVoiceEnv),
		WyomingTTSAddr:  getenv(wyomingTTSAddrEnv),
		WyomingTTSVoice: getenv(wyomingTTSVoiceEnv),
		STTProvider:     getenv(sttProviderEnv),
		OpenAISTTURL:    getenv(openaiSTTURLEnv),
		OpenAISTTKey:    getenv(openaiSTTKeyEnv),
		OpenAISTTModel:  getenv(openaiSTTModelEnv),
		WyomingSTTAddr:  getenv(wyomingSTTAddrEnv),
		WyomingSTTModel: getenv(wyomingSTTModelEnv),
		STTLanguage:     getenv(sttLanguageEnv),

		SpeakerIDURL: getenv(speakerIDURLEnv),
		SmartTurnURL: getenv(smartTurnURLEnv),
		HassURL:      getenv(hass.URLEnv),
		HassToken:    getenv(hass.TokenEnv),
	}
	cfg.BlobMinFreeMiB = getenv(blobMinFreeEnv)
	cfg.OpsListen = getenv(opsListenEnv)
	cfg.WakeArbitrationMS = getenv(wakeArbitrationEnv)
	cfg.MCPConfig = getenv(mcp.ConfigEnv)
	if cfg.Listen == "" {
		cfg.Listen = defaultListen
	}
	if cfg.OpsListen == "" {
		cfg.OpsListen = defaultOpsListen
	}
	if cfg.DSN == "" {
		cfg.DSN = journal.DefaultDSN
	}
	if cfg.LLMProvider == "" {
		cfg.LLMProvider = providerOllama
	}
	if cfg.TTSProvider == "" {
		cfg.TTSProvider = providerKokoro
	}
	if cfg.STTProvider == "" {
		cfg.STTProvider = providerSpeaches
	}
	return cfg
}

// vllmThinking reads VLLM_THINKING: off unless it says otherwise, for SPEC
// §11's budget (ADR-0077).
func (c Config) vllmThinking() (bool, error) {
	if c.VLLMThinking == "" {
		return false, nil
	}
	on, err := strconv.ParseBool(c.VLLMThinking)
	if err != nil {
		return false, fmt.Errorf("%q is not a boolean", c.VLLMThinking)
	}
	return on, nil
}

// validate names every missing variable in one error, with what it is for,
// so one restart fixes them all rather than one per restart (SPEC §13).
func (c Config) validate() error {
	type need struct{ name, value, purpose string }
	required := []need{
		{blobDirEnv, c.BlobDir, "directory for the audio the journal refers to"},
	}
	switch c.LLMProvider {
	case providerVLLM:
		required = append(required,
			need{vllmURLEnv, c.VLLMURL, "the turn engine's endpoint"},
			need{vllmModelEnv, c.VLLMModel, "the model the turn engine runs"})
		if c.EmbedModel != "" {
			// vLLM serves one model a process; the embedding model stays on
			// Ollama (ADR-0077).
			required = append(required, need{ollamaURLEnv, c.OllamaURL, "the endpoint serving " + ollamaEmbedEnv + ", since it is set"})
		}
	default:
		required = append(required,
			need{ollamaURLEnv, c.OllamaURL, "the turn engine's endpoint"},
			need{ollamaModelEnv, c.OllamaModel, "the model the turn engine runs"})
	}
	switch c.TTSProvider {
	case providerOpenAI:
		required = append(required,
			need{openaiTTSURLEnv, c.OpenAITTSURL, "the speech synthesis endpoint"},
			need{openaiTTSModelEnv, c.OpenAITTSModel, "the model the voice runs"},
			need{openaiTTSVoiceEnv, c.OpenAITTSVoice, "the voice that speaks"})
	case providerWyoming:
		required = append(required,
			need{wyomingTTSAddrEnv, c.WyomingTTSAddr, "the speech synthesis server, host:port"},
			need{wyomingTTSVoiceEnv, c.WyomingTTSVoice, "the voice that speaks"})
	default:
		required = append(required, need{kokoroURLEnv, c.KokoroURL, "the speech synthesis endpoint"})
	}
	switch c.STTProvider {
	case providerOpenAI:
		required = append(required,
			need{openaiSTTURLEnv, c.OpenAISTTURL, "the speech recognition endpoint"},
			need{openaiSTTModelEnv, c.OpenAISTTModel, "the model the ear runs"})
	case providerWyoming:
		required = append(required,
			need{wyomingSTTAddrEnv, c.WyomingSTTAddr, "the speech recognition server, host:port"},
			need{wyomingSTTModelEnv, c.WyomingSTTModel, "the model the server runs, as the journal records it"})
	default:
		required = append(required, need{sttURLEnv, c.STTURL, "the speech recognition endpoint"})
	}
	var missing []string
	if c.LLMProvider != providerOllama && c.LLMProvider != providerVLLM {
		missing = append(missing, fmt.Sprintf("  %-20s %q is not a turn engine: %s or %s", llmProviderEnv, c.LLMProvider, providerOllama, providerVLLM))
	}
	if c.TTSProvider != providerKokoro && c.TTSProvider != providerOpenAI && c.TTSProvider != providerWyoming {
		missing = append(missing, fmt.Sprintf("  %-20s %q is not a voice: %s, %s or %s", ttsProviderEnv, c.TTSProvider, providerKokoro, providerOpenAI, providerWyoming))
	}
	if c.STTProvider != providerSpeaches && c.STTProvider != providerOpenAI && c.STTProvider != providerWyoming {
		missing = append(missing, fmt.Sprintf("  %-20s %q is not an ear: %s, %s or %s", sttProviderEnv, c.STTProvider, providerSpeaches, providerOpenAI, providerWyoming))
	}
	// A Wyoming address without its port fails on the first utterance, not
	// at startup, unless it is read here.
	for _, a := range []struct{ name, provider, value string }{
		{wyomingTTSAddrEnv, c.TTSProvider, c.WyomingTTSAddr},
		{wyomingSTTAddrEnv, c.STTProvider, c.WyomingSTTAddr},
	} {
		if a.provider == providerWyoming && a.value != "" {
			if _, err := wyoming.Address(a.value); err != nil {
				missing = append(missing, fmt.Sprintf("  %-20s %v", a.name, err))
			}
		}
	}
	if _, err := c.vllmThinking(); err != nil {
		missing = append(missing, fmt.Sprintf("  %-20s %v", vllmThinkingEnv, err))
	}
	if _, err := c.speakerAccept(); err != nil {
		missing = append(missing, fmt.Sprintf("  %-20s %v", speakerAcceptEnv, err))
	}
	if _, err := c.skipConfirmation(); err != nil {
		missing = append(missing, fmt.Sprintf("  %-20s %v", confirmEnv, err))
	}
	if _, err := c.speculate(); err != nil {
		missing = append(missing, fmt.Sprintf("  %-20s %v", speculateEnv, err))
	}
	if _, _, err := c.turnBands(); err != nil {
		missing = append(missing, fmt.Sprintf("  %-20s %v", smartTurnSureEnv+", "+smartTurnDoubtEnv, err))
	}
	for _, r := range required {
		if r.value == "" {
			missing = append(missing, fmt.Sprintf("  %-20s %s", r.name, r.purpose))
		}
	}
	if _, err := c.blobFloor(); err != nil {
		missing = append(missing, fmt.Sprintf("  %-20s %v", blobMinFreeEnv, err))
	}
	if err := c.opsListenErr(); err != nil {
		missing = append(missing, fmt.Sprintf("  %-20s %v", opsListenEnv, err))
	}
	if _, err := c.wakeArbitration(); err != nil {
		missing = append(missing, fmt.Sprintf("  %-20s %v", wakeArbitrationEnv, err))
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
	// embedder chooses what a turn recalls by relevance, once a person has
	// more than fits. Nil recalls the newest (ADR-0044).
	embedder memory.Embedder
	versions journal.Versions
	synth    satellite.Synth
	stt      stt.Transcriber
	speakers listen.Speakers
	judge    listen.Judge
	// sure and doubt band the judge's probability; zero keeps listen's
	// defaults (ADR-0094).
	sure, doubt float64
	household   []string
	// skipConfirm runs a held call as written, for a household that turned
	// the yes off (ADR-0091).
	skipConfirm bool
	// speculate answers each turn in its pause (ADR-0095).
	speculate bool
	tools     map[string]session.Tool
	// specs is what the session enforces policy from: the registry's, and
	// any provided tools'. Nil is the registry's alone.
	specs map[string]registry.ToolSpec
}

// buildProviders constructs the providers without touching the network: a
// client is a URL and a config until its first request. ids is the enrolled
// household, or nil when nobody is. extra is tools provided at runtime, nil
// for none: they are offered beside the registry's, and so are part of the
// tool-schema version every event records (SPEC §8).
//
// The optional sidecars degrade rather than fail, and never silently: a
// household without them is a house that still answers (SPEC §4.5, §5, §7).
func buildProviders(cfg Config, ids *identity.Identities, log *slog.Logger, extra provided) (providers, error) {
	enforced, offered, err := offerable(extra)
	if err != nil {
		return providers{}, err
	}
	engine, err := turnEngine(cfg, offered)
	if err != nil {
		return providers{}, err
	}
	synth, err := speechVoice(cfg)
	if err != nil {
		return providers{}, err
	}
	transcriber, err := speechEar(cfg)
	if err != nil {
		return providers{}, err
	}
	// The engine knows the model, prompt and tool schema; which ear and voice
	// are in effect is a wiring fact only this function holds (SPEC §8).
	versions := engine.Versions()
	versions.STT, versions.TTS = transcriber.Version(), synth.Version()
	p := providers{
		engine: engine, summarizer: engine, versions: versions, synth: synth, stt: transcriber,
		tools: map[string]session.Tool{}, specs: enforced,
	}

	if cfg.SpeakerIDURL == "" {
		// Said here because the symptoms are a house that never answers a
		// wake (listen's confirm fails closed on an unidentified speaker)
		// and one the television can interrupt, and nothing else explains
		// either (ADR-0031).
		log.Warn("speaker identification is off: "+speakerIDURLEnv+" is not set, so every speaker is a guest and no wake opens a session, a button press included, until it is; barge-in gates on energy and partial length alone: any voice loud enough and long enough interrupts, the television included",
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
		accept, err := cfg.speakerAccept()
		if err != nil {
			return providers{}, err
		}
		resolver, err := identity.NewResolver(emb, ids, identity.Thresholds{Accept: accept})
		if err != nil {
			return providers{}, err
		}
		p.speakers, p.household = resolver, ids.Household()
		if len(p.household) == 0 {
			log.Warn("nobody is enrolled: every wake, a button press included, is rejected until a household voice is enrolled")
		}
	}

	if cfg.EmbedModel == "" {
		log.Warn("memory relevance is off: " + ollamaEmbedEnv + " is not set, so a turn is told a person's newest 20 memories and newest 5 conversations of the past week, whatever was asked")
	} else {
		emb, err := ollama.NewEmbedder(ollama.EmbedConfig{BaseURL: cfg.OllamaURL, Model: cfg.EmbedModel})
		if err != nil {
			return providers{}, err
		}
		p.embedder = emb
	}

	if cfg.SmartTurnURL == "" {
		log.Warn("semantic endpointing is off: " + smartTurnURLEnv + " is not set, so every turn ends after 800 ms of quiet: the household waits that long for every answer, and a longer pause mid-sentence cuts the speaker off")
	} else {
		judge, err := smartturn.New(smartturn.Config{BaseURL: cfg.SmartTurnURL})
		if err != nil {
			return providers{}, err
		}
		if p.sure, p.doubt, err = cfg.turnBands(); err != nil {
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
	if extra != nil {
		maps.Copy(p.tools, extra.Tools())
	}
	skip, err := cfg.skipConfirmation()
	if err != nil {
		return providers{}, err
	}
	p.skipConfirm = skip
	if p.speculate, err = cfg.speculate(); err != nil {
		return providers{}, err
	}

	return p, nil
}

// llmEngine is the engine CHORUS_LLM_PROVIDER chooses: it answers turns
// and writes each conversation's summary, offering the tools offered.
type llmEngine interface {
	session.Engine
	session.Summarizer
	Versions() journal.Versions
}

// turnEngine builds the chosen provider's engine, without touching the
// network.
func turnEngine(cfg Config, offered map[string]registry.ToolSpec) (llmEngine, error) {
	if cfg.LLMProvider != providerVLLM {
		return ollama.New(ollama.Config{BaseURL: cfg.OllamaURL, Model: cfg.OllamaModel, Specs: offered})
	}
	thinking, err := cfg.vllmThinking()
	if err != nil {
		return nil, err
	}
	return vllm.New(vllm.Config{BaseURL: cfg.VLLMURL, Model: cfg.VLLMModel, APIKey: cfg.VLLMAPIKey, Thinking: thinking, Specs: offered})
}

// versioned is a provider that names itself for the journal (SPEC §8).
type versioned interface{ Version() string }

// synthesizer is the voice CHORUS_TTS_PROVIDER chooses.
type synthesizer interface {
	satellite.Synth
	versioned
}

// transcriber is the ear CHORUS_STT_PROVIDER chooses.
type transcriber interface {
	stt.Transcriber
	versioned
}

// speechVoice builds the chosen voice, without touching the network.
func speechVoice(cfg Config) (synthesizer, error) {
	switch cfg.TTSProvider {
	case providerOpenAI:
		return openai.NewSynth(openai.SynthConfig{BaseURL: cfg.OpenAITTSURL, APIKey: cfg.OpenAITTSKey, Model: cfg.OpenAITTSModel, Voice: cfg.OpenAITTSVoice})
	case providerWyoming:
		return wyoming.NewSynth(wyoming.SynthConfig{Address: cfg.WyomingTTSAddr, Voice: cfg.WyomingTTSVoice})
	}
	return kokoro.New(kokoro.Config{BaseURL: cfg.KokoroURL})
}

// speechEar builds the chosen ear, without touching the network.
func speechEar(cfg Config) (transcriber, error) {
	switch cfg.STTProvider {
	case providerOpenAI:
		return openai.NewTranscriber(openai.TranscriberConfig{BaseURL: cfg.OpenAISTTURL, APIKey: cfg.OpenAISTTKey, Model: cfg.OpenAISTTModel, Language: cfg.STTLanguage})
	case providerWyoming:
		return wyoming.NewTranscriber(wyoming.TranscriberConfig{Address: cfg.WyomingSTTAddr, Model: cfg.WyomingSTTModel, Language: cfg.STTLanguage})
	}
	return speaches.New(speaches.Config{BaseURL: cfg.STTURL, Language: cfg.STTLanguage})
}

func enrolled(ids *identity.Identities) int {
	if ids == nil {
		return 0
	}
	return len(ids.People)
}

// speakerAccept reads CHORUS_SPEAKER_ACCEPT: zero keeps
// identity.DefaultAccept. A value at or over full correlation would accept
// nobody and a negative one everybody, so both are refused at startup rather
// than at the first word.
func (c Config) speakerAccept() (float64, error) {
	if c.SpeakerAccept == "" {
		return 0, nil
	}
	v, err := strconv.ParseFloat(c.SpeakerAccept, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", c.SpeakerAccept)
	}
	if v <= 0 || v >= 1 {
		return 0, fmt.Errorf("%v is not a cosine between 0 and 1", v)
	}
	return v, nil
}

// speculate reads CHORUS_SPECULATE: "on" answers a turn in its pause,
// unset or "off" at its end. Anything else is refused rather than guessed.
func (c Config) speculate() (bool, error) {
	switch strings.ToLower(strings.TrimSpace(c.Speculate)) {
	case "", "off", "false", "no":
		return false, nil
	case "on", "true", "yes":
		return true, nil
	}
	return false, fmt.Errorf("%q is not on or off", c.Speculate)
}

// turnBands reads CHORUS_SMARTTURN_SURE and CHORUS_SMARTTURN_DOUBT: zero
// keeps listen's default for either. A band outside 0 to 1 never or always
// fires, and doubt above sure leaves no probability its band, so all three
// are refused at startup rather than at the first pause. Equal, they are the
// single cut chorusd had before the bands (ADR-0094).
func (c Config) turnBands() (sure, doubt float64, err error) {
	for _, b := range []struct {
		raw string
		v   *float64
	}{{c.SmartTurnSure, &sure}, {c.SmartTurnDoubt, &doubt}} {
		if b.raw == "" {
			continue
		}
		v, err := strconv.ParseFloat(b.raw, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("%q is not a number", b.raw)
		}
		if v <= 0 || v >= 1 {
			return 0, 0, fmt.Errorf("%v is not a probability between 0 and 1", v)
		}
		*b.v = v
	}
	s, d := cmp.Or(sure, listen.DefaultSure), cmp.Or(doubt, listen.DefaultDoubt)
	if d > s {
		return 0, 0, fmt.Errorf("doubt %v is above sure %v", d, s)
	}
	return sure, doubt, nil
}

// skipConfirmation reads CHORUS_CONFIRM: unset or "on" holds a call for the
// person's yes, "off" runs it as written. Anything else is refused rather
// than guessed, because guessing wrong on this one runs an unasked-for call.
func (c Config) skipConfirmation() (bool, error) {
	switch strings.ToLower(strings.TrimSpace(c.Confirm)) {
	case "", "on", "true", "yes":
		return false, nil
	case "off", "false", "no":
		return true, nil
	}
	return false, fmt.Errorf("%q is not on or off", c.Confirm)
}
