//go:build !(js && wasm)

// Command reviewui serves the review screens over the journal (SPEC §9.2):
// harvested barge-in candidates, the pair flow, the barge-in timeline with
// each clip playable, verdicts in the curation table, and Replay. It reads
// CHORUS_POSTGRES_DSN and CHORUS_BLOB_DIR like chorusd, and migrates its
// schemas on start; OLLAMA_URL and OLLAMA_MODEL, or VLLM_URL and VLLM_MODEL
// under CHORUS_LLM_PROVIDER=vllm, let Replay re-run turns; SPEAKERID_URL lets
// Voices enroll the household into identities.yaml.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/contexts"
	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/erasure"
	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/memory"
	"github.com/teagan42/chorus/internal/provider/speaches"
	"github.com/teagan42/chorus/internal/provider/speakerid"
	"github.com/teagan42/chorus/internal/stt"
)

// blobDirEnv is where the journal's audio lives, the same variable chorusd
// writes through (.env.example).
const blobDirEnv = "CHORUS_BLOB_DIR"

// version is set by the release build (-X main.version=...).
var version = "dev"

// defaultAddr is loopback: there is no login, and the UI plays every
// recording in the house, so reaching it from another machine is a choice
// (-addr :8080) rather than the default (docs/reviewui/README.md).
const defaultAddr = "127.0.0.1:8080"

func main() {
	addr := flag.String("addr", defaultAddr, "listen address")
	hosts := flag.String("hosts", os.Getenv(hostsEnv), "comma-separated host names and IPs the UI answers as; default: the listen address's host, or localhost, 127.0.0.1 and ::1 when it binds loopback or every interface ($"+hostsEnv+")")
	readOnlyDefault, err := parseReadOnly(os.Getenv(readOnlyEnv))
	if err != nil {
		fmt.Fprintf(os.Stderr, "reviewui: %v\n", err)
		os.Exit(2)
	}
	identities := flag.String("identities", identity.DefaultPath, "the household's voiceprints, the file chorusd reads at startup; the Voices screen lists, enrolls and removes them")
	speakerIDURL := flag.String("speakerid-url", os.Getenv(speakerIDURLEnv), "speaker-ID sidecar base URL the Voices screen enrolls through; unset, it enrolls nobody ($"+speakerIDURLEnv+")")
	sttURL := flag.String("stt-url", os.Getenv(sttURLEnv), "speaches base URL the Voices screen transcribes a satellite's clips with, so a reviewer reads what was said before ticking it; unset, the clips are listed without words ($"+sttURLEnv+")")
	devices := flag.String("devices", os.Getenv(devicesEnv), "the satellite inventory chorusd reads, for each satellite's own speaker_accept; unset, the Voices screen judges every clip at the house's level ($"+devicesEnv+")")
	readOnly := flag.Bool("read-only", readOnlyDefault, "hide and refuse every deletion: conversations, clips, memories and summaries ($"+readOnlyEnv+")")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	allowed := parseHosts(*hosts)
	if len(allowed) == 0 {
		allowed = defaultHosts(*addr)
	}
	v := enrollment(*identities, *speakerIDURL, *sttURL)
	if v.house, v.satAccept, err = acceptLevels(os.Getenv(speakerAcceptEnv), *devices); err != nil {
		fmt.Fprintf(os.Stderr, "reviewui: %v\n", err)
		os.Exit(2)
	}
	if err := run(ctx, *addr, allowed, *readOnly, v); err != nil {
		fmt.Fprintf(os.Stderr, "reviewui: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, addr string, hosts []string, readOnly bool, v voices) error {
	log.Printf("reviewui %s", version)
	pool, err := pgxpool.New(ctx, journal.DSN())
	if err != nil {
		return fmt.Errorf("connect journal: %w", err)
	}
	defer pool.Close()
	if err := journal.Migrate(ctx, pool); err != nil {
		return err
	}
	if err := curation.Migrate(ctx, pool); err != nil {
		return err
	}
	if err := memory.Migrate(ctx, pool); err != nil {
		return err
	}
	if err := erasure.Migrate(ctx, pool); err != nil {
		return err
	}
	// Created here too, so a review box started before chorusd ever ran
	// reads an empty table rather than failing.
	if err := contexts.Migrate(ctx, pool); err != nil {
		return err
	}
	blobs, err := blob.NewDir(os.Getenv(blobDirEnv))
	if err != nil {
		return fmt.Errorf("%s: %w", blobDirEnv, err)
	}

	s := newServer(pgJournal{journal.NewPgStore(pool)}, curation.NewPgStore(pool), blobs, time.Now)
	s.engineFor = llmEngines(os.Getenv)
	s.memories, s.erasures, s.readOnly = memory.NewPgStore(pool), erasure.NewPgStore(pool), readOnly
	s.kept = contexts.NewPgStore(pool)
	s.voices = v
	srv := &http.Server{Addr: addr, Handler: s.handler(hosts), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("Browse on %s; answering as %s", browseURL(addr), strings.Join(hosts, ", "))
	if readOnly {
		log.Printf("read-only: every deletion is hidden and refused")
	}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// enrollment is the Voices screen's household file and, when a sidecar is
// named, the real client at its defaults: TitaNet-L, 192 dims, so a sidecar
// serving anything else is refused on the first take.
func enrollment(identities, speakerIDURL, sttURL string) voices {
	v := voices{identities: identities}
	if speakerIDURL != "" {
		v.embedder = func() (identity.Embedder, error) {
			return speakerid.New(speakerid.Config{BaseURL: speakerIDURL})
		}
	}
	// speaches only: the default ear (ADR-0081). A household on another one
	// reads its clips without words rather than through the wrong endpoint.
	if sttURL != "" {
		v.words = func() (stt.Transcriber, error) {
			return speaches.New(speaches.Config{BaseURL: sttURL})
		}
	}
	return v
}

// acceptLevels reads the levels chorusd matches speakers at: the house's
// from CHORUS_SPEAKER_ACCEPT and each satellite's from the inventory at
// devices, when one is named. Both are refused here as chorusd refuses them,
// so the screen never judges against a number the daemon would not start
// with (ADR-0084, ADR-0098).
func acceptLevels(house, devices string) (float64, map[string]float64, error) {
	var level float64
	if house != "" {
		v, err := strconv.ParseFloat(house, 64)
		if err != nil || v <= 0 || v >= 1 {
			return 0, nil, fmt.Errorf("%s=%q is not a cosine between 0 and 1", speakerAcceptEnv, house)
		}
		level = v
	}
	if devices == "" {
		return level, nil, nil
	}
	inv, err := config.Load(devices)
	if err != nil {
		return 0, nil, fmt.Errorf("-devices: %w", err)
	}
	per := map[string]float64{}
	for _, sat := range inv.Satellites {
		if sat.SpeakerAccept > 0 {
			per[sat.Name] = sat.SpeakerAccept
		}
	}
	return level, per, nil
}

// pgJournal names the two read interfaces PgStore already satisfies.
type pgJournal struct{ *journal.PgStore }
