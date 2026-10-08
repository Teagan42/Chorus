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
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/teaganglenn/chorus/internal/blob"
	"github.com/teaganglenn/chorus/internal/config"
	"github.com/teaganglenn/chorus/internal/esphome"
	"github.com/teaganglenn/chorus/internal/identity"
	"github.com/teaganglenn/chorus/internal/journal"
)

// version is set by the release build (-X main.version=...).
var version = "dev"

func main() {
	devices := flag.String("devices", "devices.yaml", "satellite inventory path")
	identities := flag.String("identities", "", "enrolled voiceprints path (default: identities.yaml beside the inventory; absent means nobody is enrolled)")
	flag.Parse()

	cfg := configFromEnv(os.Getenv)
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
	prov, err := buildProviders(cfg, ids, log)
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

	blobs, err := blob.NewDir(cfg.BlobDir)
	if err != nil {
		return fmt.Errorf("%s: %w", blobDirEnv, err)
	}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen for satellites (%s): %w", listenEnv, err)
	}
	log.Info("chorusd listening", "version", version, "addr", ln.Addr().String(),
		"satellites", len(inv.Satellites), "model", prov.versions.Model)

	return run(ctx, inv, deps{
		Listener:  ln,
		Native:    esphomeDialer{},
		Store:     journal.NewPgStore(pool),
		Blobs:     blobs,
		Clock:     wallClock{},
		Timers:    wallTimers{},
		providers: prov,
		Log:       log,
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
