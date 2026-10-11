package main

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
)

// serveAs sends one GET for the Curate page to h, addressed to host the way
// a browser would put it in the Host header.
func serveAs(h http.Handler, host string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/curate/pairs", nil)
	r.Host = host
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// The review box plays every recording in the house, so out of the box it
// listens where only the box's own browser can reach it.
//
// verifies SPEC §1
func TestTheUIListensOnLoopbackUnlessTold(t *testing.T) {
	if defaultAddr != "127.0.0.1:8080" {
		t.Errorf("default -addr is %q; the guest Wi-Fi can reach anything but loopback", defaultAddr)
	}
	for _, c := range []struct {
		addr string
		want []string
	}{
		{defaultAddr, loopbackHosts},
		{":8080", loopbackHosts},
		{"0.0.0.0:8080", loopbackHosts},
		{"[::]:8080", loopbackHosts},
		{"[::1]:8080", loopbackHosts},
		{"localhost:9090", loopbackHosts},
		{"10.0.0.5:8080", []string{"10.0.0.5"}},
		{"review.home:8080", []string{"review.home"}},
	} {
		if got := defaultHosts(c.addr); !slices.Equal(got, c.want) {
			t.Errorf("defaultHosts(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
	for _, c := range []struct{ addr, want string }{
		{defaultAddr, "http://127.0.0.1:8080/conversations"},
		{":9090", "http://localhost:9090/conversations"},
		{"[::]:8080", "http://localhost:8080/conversations"},
		{"review.home:8080", "http://review.home:8080/conversations"},
	} {
		if got := browseURL(c.addr); got != c.want {
			t.Errorf("browseURL(%q) = %q, want %q", c.addr, got, c.want)
		}
	}
}

// -hosts takes the names as people type them: with a port, in brackets,
// with spaces after the commas.
func TestHostsListReadsAsTyped(t *testing.T) {
	got := parseHosts(" review.home, 10.0.0.5:8080 ,[::1], Kitchen-Pi.local,")
	want := []string{"review.home", "10.0.0.5", "::1", "kitchen-pi.local"}
	if !slices.Equal(got, want) {
		t.Errorf("parseHosts = %v, want %v", got, want)
	}
	if got := parseHosts(""); got != nil {
		t.Errorf("parseHosts(\"\") = %v, want nothing, so the default applies", got)
	}
}

// A recipe site Alan has open re-points its own domain at the box's LAN
// address. To the browser that page is now same-origin with the review UI,
// so the cross-origin check cannot tell; the Host header still names the
// recipe site, and that is refused before any route runs.
//
// verifies SPEC §1, §9.2
func TestARequestAddressedToAForeignHostIsRefused(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.handler(defaultHosts(defaultAddr))

	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	for _, host := range []string{"127.0.0.1:8080", "localhost:8080", "LOCALHOST", "[::1]:8080"} {
		if w := serveAs(h, host); w.Code != http.StatusOK {
			t.Errorf("Host %q = %d, want the page: %s", host, w.Code, w.Body.String())
		}
	}
	for _, host := range []string{"recipes.example", "recipes.example:8080", "10.0.0.5:8080", ""} {
		w := serveAs(h, host)
		if w.Code != http.StatusMisdirectedRequest {
			t.Errorf("Host %q = %d, want 421", host, w.Code)
		}
		if body := w.Body.String(); !strings.Contains(body, "-hosts") || !strings.Contains(body, "localhost, 127.0.0.1, ::1") {
			t.Errorf("Host %q refusal does not say how to allow a name: %q", host, body)
		}
	}

	// The second and third request from the same name are not news.
	serveAs(h, "recipes.example")
	serveAs(h, "recipes.example")
	lines := strings.Count(logged.String(), "recipes.example")
	if lines != 1 {
		t.Errorf("recipes.example was logged %d times, want once:\n%s", lines, logged.String())
	}
	if !strings.Contains(logged.String(), `"10.0.0.5:8080"`) {
		t.Errorf("the LAN name was never logged:\n%s", logged.String())
	}
}

// Told the box's own name, the UI answers to it and to nothing else: a
// household that exposes it deliberately still does not answer a stranger's.
//
// verifies SPEC §1
func TestTheUIAnswersAsTheNamesItWasGiven(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.handler(parseHosts("review.home, 10.0.0.5"))
	for _, host := range []string{"review.home:8080", "10.0.0.5:8080", "Review.Home"} {
		if w := serveAs(h, host); w.Code != http.StatusOK {
			t.Errorf("Host %q = %d, want the page", host, w.Code)
		}
	}
	for _, host := range []string{"localhost:8080", "127.0.0.1:8080", "recipes.example"} {
		if w := serveAs(h, host); w.Code != http.StatusMisdirectedRequest {
			t.Errorf("Host %q = %d, want 421", host, w.Code)
		}
	}
}

// The UI's own posts land on the hardened handler the way they do on the
// bare routes: the allow-list sits in front of the cross-origin check, not
// in place of it.
//
// verifies SPEC §9.2
func TestTheAllowListKeepsTheCrossOriginCheck(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.handler(defaultHosts(defaultAddr))
	r := httptest.NewRequest(http.MethodPost, "/pairs/"+pairID+"/accept", nil)
	r.Host = "127.0.0.1:8080"
	r.Header.Set("Origin", "http://recipes.example")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("a cross-site post through the allow-list = %d, want 403", w.Code)
	}
}

// getAs is one GET to h addressed to the box's own name.
func getAs(h http.Handler, target string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.Host = "127.0.0.1:8080"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// Every screen, and everything a screen loads, comes from this server: a
// review box tells no third party which evening someone sat down to listen.
//
// verifies SPEC §1
func TestNoScreenReachesForGoogle(t *testing.T) {
	s, _ := newHouseholdServer(t)
	h := s.handler(defaultHosts(defaultAddr))
	for _, target := range []string{
		"/conversations", "/conversations/" + convZeppel, "/queue", "/review", "/replays",
		"/replays/" + convZeppel, "/curate/pairs", "/export", "/static/css/chorus.css", "/static/css/fonts.css",
	} {
		w := getAs(h, target)
		if w.Code != http.StatusOK {
			t.Errorf("GET %s = %d", target, w.Code)
		}
		for _, leak := range []string{"googleapis", "gstatic", "unpkg.com", "https://"} {
			if strings.Contains(w.Body.String(), leak) {
				t.Errorf("GET %s names %s", target, leak)
			}
		}
	}
	for file, typ := range map[string]string{
		"/static/fonts/Outfit-Variable.ttf":         "font/ttf",
		"/static/fonts/JetBrainsMono-Regular.woff2": "font/woff2",
		"/static/fonts/JetBrainsMono-Medium.woff2":  "font/woff2",
	} {
		w := getAs(h, file)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != typ {
			t.Errorf("GET %s = %d %s, want 200 %s", file, w.Code, w.Header().Get("Content-Type"), typ)
		}
	}
}

// Every response tells the browser the rules: no page may frame the UI (a
// recipe site cannot lay an invisible Curate over its buttons), nothing is
// sniffed into a script, nothing but this server supplies a script, style,
// font or clip, and no address leaks in a Referer. A screen, a clip and a
// refusal all carry them.
//
// verifies SPEC §1, §9.2
func TestEveryResponseCarriesTheSecurityHeaders(t *testing.T) {
	s, _ := newTestServer(t)
	h := s.handler(defaultHosts(defaultAddr))
	for _, c := range []struct {
		target string
		status int
	}{
		{"/curate/pairs", http.StatusOK},
		{"/conversations/conv-1", http.StatusOK},
		{"/audio?ref=blob://mic/1", http.StatusOK},
		{"/static/css/chorus.css", http.StatusOK},
		{"/conversations?day=thursday", http.StatusBadRequest},
		{"/conversations/conv-0000-attic", http.StatusNotFound},
	} {
		w := getAs(h, c.target)
		if w.Code != c.status {
			t.Errorf("GET %s = %d, want %d", c.target, w.Code, c.status)
		}
		for k, v := range map[string]string{
			"X-Frame-Options":        "DENY",
			"X-Content-Type-Options": "nosniff",
			"Referrer-Policy":        "no-referrer",
		} {
			if got := w.Header().Get(k); got != v {
				t.Errorf("GET %s: %s = %q, want %q", c.target, k, got, v)
			}
		}
		policy := w.Header().Get("Content-Security-Policy")
		for _, rule := range []string{
			"default-src 'self'", "script-src 'self';", "media-src 'self'", "font-src 'self'",
			"img-src 'self'", "connect-src 'self'", "style-src-elem 'self'", "frame-ancestors 'none'",
			"object-src 'none'", "base-uri 'none'", "form-action 'self'",
		} {
			if !strings.Contains(policy+";", rule) {
				t.Errorf("GET %s: the policy lacks %q: %s", c.target, rule, policy)
			}
		}
		if strings.Contains(policy, "unsafe-eval") || strings.Contains(policy, "script-src 'self' 'unsafe-inline'") {
			t.Errorf("GET %s: the policy lets a script in that this server did not send: %s", c.target, policy)
		}
	}
	// The refusal of a foreign name is a response too.
	if w := serveAs(h, "recipes.example"); w.Header().Get("X-Frame-Options") != "DENY" || w.Header().Get("Content-Security-Policy") == "" {
		t.Errorf("the 421 carries %v", w.Header())
	}
}

// script-src 'self' holds only if no screen carries a script of its own:
// no inline <script>, no on* handler, no hx-on, nothing htmx must eval.
//
// verifies SPEC §9.2
func TestNoScreenCarriesAnInlineScript(t *testing.T) {
	s, _ := newHouseholdServer(t)
	h := s.handler(defaultHosts(defaultAddr))
	for _, target := range []string{
		"/conversations", "/conversations/" + convZeppel, "/conversations/" + convDoor, "/queue", "/review",
		"/replays", "/replays/" + convZeppel, "/curate/pairs", "/export",
	} {
		body := getAs(h, target).Body.String()
		for _, tag := range strings.Split(body, "<script")[1:] {
			if !strings.HasPrefix(tag, ` src="/static/`) {
				t.Errorf("GET %s carries a script of its own: <script%.60s", target, tag)
			}
		}
		for _, inline := range []string{" onclick=", " onload=", " onerror=", " hx-on", "javascript:", `hx-vals="js:`, "<style"} {
			if strings.Contains(body, inline) {
				t.Errorf("GET %s carries %q, which the policy refuses", target, inline)
			}
		}
	}
}
