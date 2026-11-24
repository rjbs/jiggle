package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// answers checks the status of the answer to a request of the given method
// and path, with the given headers, made of a server with an empty library.
// The request is addressed to localhost unless it has a Host header.
func answers(t *testing.T, method, path string, headers map[string]string, want int) {
	t.Helper()
	f := newFixture(t)
	req := httptest.NewRequest(method, path, strings.NewReader(`{"using":[],"methodCalls":[]}`))
	req.Host = "localhost"
	for name, value := range headers {
		if name == "Host" {
			req.Host = value
			continue
		}
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
	answers(t, "POST", "/jmap/api", map[string]string{"Origin": "http://localhost"}, 200)

	// A page from somewhere else, in a browser new or old.
	answers(t, "POST", "/jmap/api", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403)
	answers(t, "POST", "/jmap/api", map[string]string{"Origin": "http://elsewhere.example"}, 403)
	answers(t, "POST", "/jmap/upload/photos/", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403)

	// It may ask for things, which the browser won't let it read.
	answers(t, "GET", "/.well-known/jmap", map[string]string{"Sec-Fetch-Site": "cross-site"}, 200)
}

// The editor and the API both refuse a request addressed to a name that
// isn't the server's, which is what a page gets up to by DNS rebinding.
func TestOtherNames(t *testing.T) {
	answers(t, "GET", "/", map[string]string{"Host": "127.0.0.1:8739"}, 200)
	answers(t, "GET", "/", map[string]string{"Host": "attacker.example"}, 421)
	answers(t, "GET", "/.well-known/jmap", map[string]string{"Host": "attacker.example"}, 421)
}
