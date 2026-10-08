// Command reviewui serves the Curate and Review screens over the journal
// (SPEC §9.2): harvested barge-in candidates, the pair flow, the barge-in
// timeline with each clip playable, verdicts in the curation table. It
// reads CHORUS_POSTGRES_DSN and CHORUS_BLOB_DIR like chorusd, and migrates
// both schemas on start.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teaganglenn/chorus/internal/blob"
	"github.com/teaganglenn/chorus/internal/curation"
	"github.com/teaganglenn/chorus/internal/journal"
)

// blobDirEnv is where the journal's audio lives, the same variable chorusd
// writes through (.env.example).
const blobDirEnv = "CHORUS_BLOB_DIR"

// version is set by the release build (-X main.version=...).
var version = "dev"

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, *addr); err != nil {
		fmt.Fprintf(os.Stderr, "reviewui: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, addr string) error {
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
	srv := &http.Server{Addr: addr, Handler: s.routes(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("Curate on http://localhost%s%s", addr, "/curate/pairs")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// pgJournal names the two read interfaces PgStore already satisfies.
type pgJournal struct{ *journal.PgStore }
