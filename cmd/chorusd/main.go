// Command chorusd is the orchestrator daemon: the TCP server every satellite
// dials for audio, one session supervisor per link over a shared journal and
// conversation index, and the native API client that keeps each device's
// control connection alive (SPEC §2, §13).
//
// Everything that reads a clock or does I/O reaches run through deps, so the
// hermetic tier runs the whole daemon against in-memory doubles; main is the
// only place the real clock, sockets and database are named.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	// The household's zone comes from TZ, and the distroless image is not
	// relied on to carry the database that names it.
	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teagan42/chorus/internal/blob"
	"github.com/teagan42/chorus/internal/config"
	"github.com/teagan42/chorus/internal/contexts"
	"github.com/teagan42/chorus/internal/curation"
	"github.com/teagan42/chorus/internal/discover"
	"github.com/teagan42/chorus/internal/esphome"
	"github.com/teagan42/chorus/internal/identity"
	"github.com/teagan42/chorus/internal/journal"
	"github.com/teagan42/chorus/internal/memory"
	"github.com/teagan42/chorus/internal/ops"
	"github.com/teagan42/chorus/internal/otel"
)

// version is set by the release build (-X main.version=...).
var version = "dev"

func main() {
	devices := flag.String("devices", "devices.yaml", "satellite inventory path")
	identities := flag.String("identities", "", "enrolled voiceprints path (default: identities.yaml beside the inventory; absent means nobody is enrolled)")
	check := flag.Bool("healthcheck", false, "ask the running daemon's /healthz on $"+opsListenEnv+" and exit 0 if it is alive: the container's healthcheck, as the image has no shell")
	flag.Parse()

	cfg := configFromEnv(os.Getenv)
	if *check {
		if err := healthcheck(context.Background(), &http.Client{Timeout: ops.DefaultTimeout}, cfg.OpsListen); err != nil {
			fmt.Fprintf(os.Stderr, "chorusd: healthcheck: %v\n", err)
			os.Exit(1)
		}
		return
	}
	cfg.Devices = *devices
	cfg.Identities = *identities
	if cfg.Identities == "" {
		cfg.Identities = filepath.Join(filepath.Dir(cfg.Devices), identity.DefaultPath)
	}
	if err := cfg.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "chorusd: %v\n", err)
		os.Exit(1)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := start(ctx, cfg, log); err != nil {
		fmt.Fprintf(os.Stderr, "chorusd: %v\n", err)
		os.Exit(1)
	}
}

// start opens everything real and hands it to run. Each failure here names
// what it was opening, because a daemon that cannot start has nothing else
// to say.
func start(ctx context.Context, cfg Config, log *slog.Logger) error {
	inv, err := config.Load(cfg.Devices)
	if err != nil {
		return err
	}
	ids, err := loadIdentities(cfg.Identities, log)
	if err != nil {
		return err
	}
	// Before the engine: what the model is offered includes the servers'
	// tools, fixed once their discovery window ends (ADR-0075).
	servers, err := startMCP(ctx, cfg, os.Getenv, wallTimers{}, log)
	if err != nil {
		return err
	}
	var extra provided
	if servers != nil {
		defer servers.Close()
		extra = servers
	}
	prov, err := buildProviders(cfg, ids, log, extra)
	if err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, cfg.DSN)
	if err == nil {
		err = pool.Ping(ctx)
	}
	if err != nil {
		return fmt.Errorf("connect journal (%s): %w", journal.DSNEnv, err)
	}
	defer pool.Close()
	// Idempotent, so every start applies what a new build added and a
	// restart is a no-op (internal/journal/pgstore.go).
	if err := journal.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate journal: %w", err)
	}
	if err := memory.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate memory: %w", err)
	}
	// Retention reads what reviewers curated, so the tables must exist even
	// before the review UI has first run.
	if err := curation.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate curation: %w", err)
	}
	if err := contexts.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrate contexts: %w", err)
	}
	if err := recordOffer(ctx, contexts.NewPgStore(pool), prov); err != nil {
		return fmt.Errorf("record what the model is offered: %w", err)
	}

	dir, err := blob.NewDir(cfg.BlobDir)
	if err != nil {
		return fmt.Errorf("%s: %w", blobDirEnv, err)
	}
	// Validated with the rest of the environment, so these cannot fail.
	floor, _ := cfg.blobFloor()
	arbitration, _ := cfg.wakeArbitration()
	metrics := ops.NewMetrics(version)
	var (
		blobs = metrics.MeterBlobs(dir)
		prune = &pruneDeps{Curation: curation.NewPgStore(pool), Blobs: dir}
	)
	if floor > 0 {
		g := blob.Guard(blobs, blob.GuardConfig{
			Disk: blob.DirFree(cfg.BlobDir), Floor: floor, Clock: wallClock{}, Log: log,
			OnDecline: metrics.BlobDeclined,
		})
		blobs, prune.NotKept = g, g
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen for satellites (%s): %w", listenEnv, err)
	}
	var watch *opsDeps
	if cfg.opsEnabled() {
		opsLn, err := net.Listen("tcp", cfg.OpsListen)
		if err != nil {
			_ = ln.Close()
			return fmt.Errorf("listen for ops (%s): %w", opsListenEnv, err)
		}
		// No client timeout: each probe is bounded by readiness's own.
		watch = &opsDeps{Listener: opsLn, Metrics: metrics, Checks: opsChecks(cfg, &http.Client{}, pool.Ping)}
	} else {
		log.Warn("ops endpoints are off: " + opsListenEnv + "=off, so nothing serves health, readiness or metrics")
	}
	var traces *otel.Exporter
	if cfg.Traces.Enabled() {
		// No client timeout: each export is bounded by the exporter's own.
		traces = otel.New(otel.Config{
			Settings: cfg.Traces, Version: version, Client: &http.Client{},
			Timers: wallTimers{}, Meter: metrics, Log: log,
		})
		log.Info("exporting spans derived from the journal over OTLP/HTTP", "endpoint", cfg.Traces.Endpoint, "words", cfg.Traces.Words)
	}
	log.Info("chorusd listening", "version", version, "addr", ln.Addr().String(),
		"satellites", len(inv.Satellites), "llm", cfg.LLMProvider, "model", prov.versions.Model,
		"stt", prov.versions.STT, "tts", prov.versions.TTS, "tool_schema", prov.versions.ToolSchema,
		// What every turn is told the time in; UTC in a container without TZ.
		"zone", time.Local.String())

	return run(ctx, inv, deps{
		Listener:  ln,
		Native:    esphomeDialer{},
		Locator:   discover.New(wallTimers{}),
		Store:     journal.NewPgStore(pool),
		Memories:  memory.NewPgStore(pool),
		Blobs:     blobs,
		Prune:     prune,
		Clock:     wallClock{},
		Timers:    wallTimers{},
		providers: prov,
		Ops:       watch,
		Traces:    traces,
		Log:       log,

		WakeArbitration: arbitration,
	})
}

// loadIdentities reads the household, or reports nobody enrolled when the
// file is absent. Any other failure is a startup error: a household file
// that will not parse must not silently become a house full of guests.
func loadIdentities(path string, log *slog.Logger) (*identity.Identities, error) {
	ids, err := identity.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		log.Info("no identities file: nobody is enrolled", "path", path)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	log.Info("household loaded", "path", path, "enrolled", len(ids.People), "model", ids.Model)
	return ids, nil
}

// wallClock, wallTimers and esphomeDialer are the daemon's only wall-clock
// reads. Everything below main takes them as interfaces (CONTRIBUTING §1).
type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

type wallTimers struct{}

func (wallTimers) After(d time.Duration) <-chan time.Time { return time.After(d) }

// esphomeDialer is the real native API client, over Noise (ADR-0009). The
// deadline is set here because the Noise handshake reads the socket with no
// other way to be interrupted (internal/esphome/client.go).
type esphomeDialer struct{}

func (esphomeDialer) Dial(ctx context.Context, address, psk string) (nativeConn, error) {
	ctx, cancel := context.WithTimeout(ctx, nativeDialTimeout)
	defer cancel()
	c, err := esphome.Dial(ctx, address, psk)
	if err != nil {
		return nil, err
	}
	return nativeClient{c}, nil
}

type nativeClient struct{ *esphome.Client }

func (c nativeClient) Describe() string {
	info := c.DeviceInfoMsg
	return fmt.Sprintf("%s, esphome %s, %s %s, api %d.%d",
		c.Name(), info.GetEsphomeVersion(), info.GetManufacturer(), info.GetModel(),
		c.APIVersion[0], c.APIVersion[1])
}
