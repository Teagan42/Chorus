//go:build !(js && wasm)

// Command reviewui serves the review screens over the journal (SPEC §9.2):
// harvested barge-in candidates, the pair flow, the barge-in timeline with
// each clip playable, verdicts in the curation table, and Replay. It reads
// CHORUS_POSTGRES_DSN and CHORUS_BLOB_DIR like chorusd, and migrates both
// schemas on start; OLLAMA_URL and OLLAMA_MODEL let Replay re-run turns.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/journal"
)

// blobDirEnv is where the journal's audio lives, the same variable chorusd
// writes through (.env.example).
const blobDirEnv = "CHORUS_BLOB_DIR"

// Replay asks the same endpoint chorusd's turn engine does. Unset leaves
// Replay showing the recorded turns with nothing to re-run them against.
const (
	ollamaURLEnv   = "OLLAMA_URL"
	ollamaModelEnv = "OLLAMA_MODEL"
)

// version is set by the release build (-X main.version=...).
var version = "dev"

// defaultAddr is loopback: there is no login, and the UI plays every
// recording in the house, so reaching it from another machine is a choice
// (-addr :8080) rather than the default (docs/reviewui/README.md).
const defaultAddr = "127.0.0.1:8080"

func main() {
	addr := flag.String("addr", defaultAddr, "listen address")
	hosts := flag.String("hosts", os.Getenv(hostsEnv), "comma-separated host names and IPs the UI answers as; default: the listen address's host, or localhost, 127.0.0.1 and ::1 when it binds loopback or every interface ($"+hostsEnv+")")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	allowed := parseHosts(*hosts)
	if len(allowed) == 0 {
		allowed = defaultHosts(*addr)
	}
	if err := run(ctx, *addr, allowed); err != nil {
		fmt.Fprintf(os.Stderr, "reviewui: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, addr string, hosts []string) error {
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
	blobs, err := blob.NewDir(os.Getenv(blobDirEnv))
	if err != nil {
		return fmt.Errorf("%s: %w", blobDirEnv, err)
	}

	s := newServer(pgJournal{journal.NewPgStore(pool)}, curation.NewPgStore(pool), blobs, time.Now)
	s.engineFor = ollamaEngines(os.Getenv(ollamaURLEnv), os.Getenv(ollamaModelEnv))
	srv := &http.Server{Addr: addr, Handler: s.handler(hosts), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("Browse on %s; answering as %s", browseURL(addr), strings.Join(hosts, ", "))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// pgJournal names the two read interfaces PgStore already satisfies.
type pgJournal struct{ *journal.PgStore }
