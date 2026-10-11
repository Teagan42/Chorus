//go:build !(js && wasm)

package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
)

// hostsEnv lists the names the UI may be addressed as, like -hosts. A
// browser is sent to the box by whatever name a page gave it, so a name not
// listed is a page that re-pointed its own domain at the box (DNS rebinding),
// and the same-origin check would otherwise let it through.
const hostsEnv = "CHORUS_REVIEWUI_HOSTS"

// loopbackHosts is how a box names itself in its own browser.
var loopbackHosts = []string{"localhost", "127.0.0.1", "::1"}

// defaultHosts is what -hosts means unset: the listen address's host, or the
// loopback names when the address binds loopback or every interface.
func defaultHosts(addr string) []string {
	host := hostOnly(addr)
	ip := net.ParseIP(host)
	if host == "" || host == "localhost" || ip != nil && (ip.IsLoopback() || ip.IsUnspecified()) {
		return loopbackHosts
	}
	return []string{host}
}

// parseHosts reads a comma-separated -hosts list, each entry normalised the
// way a request's Host is compared.
func parseHosts(list string) []string {
	var out []string
	for _, h := range strings.Split(list, ",") {
		if h = hostOnly(strings.TrimSpace(h)); h != "" {
			out = append(out, h)
		}
	}
	return out
}

// hostOnly is a Host header or listen address without its port or IPv6
// brackets, lower-cased: "[::1]:8080" and "::1" name the same box.
func hostOnly(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}

// browseURL is where a person opens the UI after it starts on addr.
func browseURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr + "/conversations"
	}
	if ip := net.ParseIP(host); host == "" || ip != nil && ip.IsUnspecified() {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port) + "/conversations"
}

// hostAllowlist refuses, before routing, any request whose Host is not a
// name the UI was told it answers as. Each refused name is logged once:
// a browser retrying is not news, a new name is.
type hostAllowlist struct {
	allowed map[string]bool
	names   string
	next    http.Handler

	mu     sync.Mutex
	logged map[string]bool
}

// allowHosts wraps next so only requests addressed to one of hosts reach it.
func allowHosts(hosts []string, next http.Handler) http.Handler {
	a := &hostAllowlist{allowed: map[string]bool{}, names: strings.Join(hosts, ", "), next: next, logged: map[string]bool{}}
	for _, h := range hosts {
		a.allowed[hostOnly(h)] = true
	}
	return a
}

// loggedHostsCap bounds what a client spraying made-up names can make the
// process remember.
const loggedHostsCap = 256

func (a *hostAllowlist) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)
	if a.allowed[host] {
		a.next.ServeHTTP(w, r)
		return
	}
	a.mu.Lock()
	if !a.logged[host] {
		if len(a.logged) >= loggedHostsCap {
			a.logged = map[string]bool{}
		}
		a.logged[host] = true
		log.Printf("reviewui: refused a request addressed to %q; this UI answers as %s (-hosts adds a name)", r.Host, a.names)
	}
	a.mu.Unlock()
	http.Error(w, fmt.Sprintf("This review UI answers only as %s, and this request named %q. If that name is yours, start the UI with -hosts (docs/reviewui/README.md).", a.names, r.Host), http.StatusMisdirectedRequest)
}

// csp is as strict as the templates allow. Scripts, styles, fonts, images,
// audio and htmx's requests come from this server and nowhere else; no page
// may frame the UI; no inline script runs. Style attributes stay allowed,
// because the timeline, the day lanes and the clip tiles position hundreds
// of elements with computed left/width values, which only an attribute can
// carry; style-src-elem keeps <style> elements and stylesheets to 'self'.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; style-src-elem 'self'; " +
	"img-src 'self'; font-src 'self'; media-src 'self'; connect-src 'self'; " +
	"object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

// securityHeaders are on every response, refusals included.
var securityHeaders = map[string]string{
	"Content-Security-Policy": csp,
	"X-Frame-Options":         "DENY",
	"X-Content-Type-Options":  "nosniff",
	"Referrer-Policy":         "no-referrer",
}

// secureHeaders sets the browser's rules for every response before the
// handler writes it: no framing, no sniffing, no referrer, and the policy.
func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		for k, v := range securityHeaders {
			h.Set(k, v)
		}
		next.ServeHTTP(w, r)
	})
}

// handler is everything the real server puts in front of the screens: the
// security headers, the Host allow-list, then the routes with their
// cross-origin check. The demo build (main_js.go) has no network and
// serves routes() bare.
func (s *server) handler(hosts []string) http.Handler {
	return secureHeaders(allowHosts(hosts, s.routes()))
}
