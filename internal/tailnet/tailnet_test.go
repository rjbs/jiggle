package tailnet

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// checkAdmits sends a request through RequireLogin, allowing the given
// logins, from a node whois identifies as id (or fails to, given err), and
// checks the status that comes back.
func checkAdmits(t *testing.T, allowed []string, id Identity, err error, want int) {
	t.Helper()
	whois := func(context.Context, string) (Identity, error) { return id, err }
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	h := RequireLogin(whois, allowed, slog.New(slog.NewTextHandler(io.Discard, nil)), ok)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/jmap/session", nil))
	if rec.Code != want {
		t.Errorf("allowing %v, %+v (err %v): status %d, want %d", allowed, id, err, rec.Code, want)
	}
}

func TestRequireLogin(t *testing.T) {
	me := []string{"rjbs@github"}
	checkAdmits(t, me, Identity{Login: "rjbs@github"}, nil, http.StatusOK)
	checkAdmits(t, me, Identity{Login: "someone@github"}, nil, http.StatusForbidden)
	checkAdmits(t, me, Identity{Login: "rjbs"}, nil, http.StatusForbidden)
	checkAdmits(t, me, Identity{Login: "rjbs@github", Tagged: true}, nil, http.StatusForbidden)
	checkAdmits(t, me, Identity{}, errors.New("no such peer"), http.StatusUnauthorized)
	checkAdmits(t, nil, Identity{Login: "rjbs@github"}, nil, http.StatusForbidden)
}
