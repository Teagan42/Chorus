// Command harvest exports a conversation's barge-in corrections as DPO
// candidates (SPEC §9.1). A dev tool in the shape of probe: it reads the
// journal at CHORUS_POSTGRES_DSN, writes JSONL to stdout, and reports counts
// on stderr. PgStore has no listing, so conversation ids are arguments.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/jackc/pgx/v5"

	"github.com/teaganglenn/chorus/internal/harvest"
	"github.com/teaganglenn/chorus/internal/journal"
)

func main() {
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(),
			"usage: harvest <conversation-id>...\n\nReads %s (default: the docker-compose journal).\n", journal.DSNEnv)
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, flag.Args(), os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "harvest: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, ids []string, out, report io.Writer) error {
	conn, err := pgx.Connect(ctx, journal.DSN())
	if err != nil {
		return fmt.Errorf("connect journal: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	store := journal.NewPgStore(conn)

	for _, id := range ids {
		res, err := harvest.Scan(ctx, store, id)
		if err != nil {
			return err
		}
		if err := harvest.Export(out, res.Pairs); err != nil {
			return err
		}
		fmt.Fprintf(report, "%s: %d candidates, %d uncorrected cuts\n", id, len(res.Pairs), res.Uncorrected)
	}
	return nil
}
