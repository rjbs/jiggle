package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// answers checks the status of the answer to a request of the given method
// and path, with the given headers, made of a server with an empty library.
func answers(t *testing.T, method, path string, headers map[string]string, want int) {
	t.Helper()
	f := newFixture(t)
	req := httptest.NewRequest(method, path, strings.NewReader(`{"using":[],"methodCalls":[]}`))
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(w, req)
	if w.Code != want {
		t.Errorf("%s %s with %v: status %d, want %d", method, path, headers, w.Code, want)
	}
}

func TestCrossSiteRequests(t *testing.T) {
	// A client that isn't a browser says nothing of where it's from.
	answers(t, "POST", "/jmap/api", nil, 200)
	// The editor's own requests.
	answers(t, "POST", "/jmap/api", map[string]string{"Sec-Fetch-Site": "same-origin"}, 200)
	answers(t, "POST", "/jmap/api", map[string]string{"Origin": "http://example.com"}, 200)

	// A page from somewhere else, in a browser new or old.
	answers(t, "POST", "/jmap/api", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403)
	answers(t, "POST", "/jmap/api", map[string]string{"Origin": "http://elsewhere.example"}, 403)
	answers(t, "POST", "/jmap/upload/photos/", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403)

	// It may ask for things, which the browser won't let it read.
	answers(t, "GET", "/.well-known/jmap", map[string]string{"Sec-Fetch-Site": "cross-site"}, 200)
}

// hostIs checks whether a request addressed to host gets past OnlyHosts
// for the names given.
func hostIs(t *testing.T, names []string, host string, allowed bool) {
	t.Helper()
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	req := httptest.NewRequest("GET", "/", nil)
	req.Host = host
	w := httptest.NewRecorder()
	OnlyHosts(names, inner).ServeHTTP(w, req)
	if got := w.Code == 200; got != allowed {
		t.Errorf("a request to %q, of a server named %v: status %d, allowed %v, want %v", host, names, w.Code, got, allowed)
	}
}

func TestOnlyHosts(t *testing.T) {
	names := []string{"localhost", "", "https://photos.example.com:8443/", "jiggle"}

	hostIs(t, names, "localhost:8739", true)
	hostIs(t, names, "LOCALHOST", true)
	hostIs(t, names, "photos.example.com:8443", true)
	hostIs(t, names, "photos.example.com.", true)
	hostIs(t, names, "jiggle", true)

	// An address can't be made to mean some other machine.
	hostIs(t, names, "127.0.0.1:8739", true)
	hostIs(t, names, "[::1]:8739", true)
	hostIs(t, names, "100.64.0.1", true)

	hostIs(t, names, "attacker.example:8739", false)
	hostIs(t, names, "localhost.attacker.example", false)
	hostIs(t, names, "", false)
}
