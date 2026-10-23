// Package capabilitytest holds a provider to its declaration: what it says
// it can do, its own tests make it do (ADR-0102).
package capabilitytest

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/teagan42/chorus/internal/provider/capability"
)

// guard bounds a wait that only a broken provider reaches. It is a hang
// detector, not a timing, so it reads the wall clock.
const guard = 5 * time.Second

// Hung is a server that takes every request and answers none, until the
// client gives up on it. arrived receives as each request reaches it.
func Hung(t *testing.T) (url string, arrived <-chan struct{}) {
	t.Helper()
	got, release := make(chan struct{}, 1), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case got <- struct{}{}:
		default:
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	return srv.URL, got
}

// HungTCP is Hung for a provider that speaks TCP itself, as Wyoming does:
// it accepts, reads what it is sent and writes nothing back.
func HungTCP(t *testing.T) (addr string, arrived <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{}, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				buf := make([]byte, 4096)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
					select {
					case got <- struct{}{}:
					default:
					}
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String(), got
}

// Cancels cancels call once its request has reached a hung server, and
// fails unless it then returns an error at once, when d declares that it
// cancels. A provider that declares it does not has nothing to prove.
func Cancels(t *testing.T, d capability.Declaration, arrived <-chan struct{}, call func(context.Context) error) {
	t.Helper()
	if !d.Cancels {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- call(ctx) }()
	select {
	case <-arrived:
	case err := <-done:
		t.Fatalf("%s %s returned before its request reached the server: %v", d.Role, d.Name, err)
	case <-time.After(guard):
		t.Fatalf("%s %s never reached the server", d.Role, d.Name)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("%s %s returned no error for a request it never got an answer to", d.Role, d.Name)
		}
	case <-time.After(guard):
		t.Fatalf("%s %s declares it cancels, and was still waiting %v after its context was cancelled", d.Role, d.Name, guard)
	}
}
