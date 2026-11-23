package server

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// Who may use the server is decided by where a request comes from: this
// machine, or a node of the tailnet that someone allowed is logged in to.
// A web browser there will send requests for any page that asks it to, so
// a request from the right place is not enough.  These two guards are
// against that.  Neither affects a client that isn't a browser.
// -- claude, 2026-11-23

// sameOriginOnly refuses requests that change something and that a browser
// sent for a page from somewhere else.  The API reads a request's body
// whatever its content type, so without this any page open in the browser
// could destroy photos, though it couldn't read anything back.
func sameOriginOnly(next http.Handler) http.Handler {
	return http.NewCrossOriginProtection().Handler(next)
}

// OnlyHosts refuses requests addressed to a host that is neither an IP
// address nor one of the names given, each of which may be a host name or a
// URL that has one.  Empty names are ignored.
//
// A page from anywhere can have its own name made to resolve to this
// machine's address, or the server's on the tailnet, and then the browser
// takes the server for the page's own site, and lets the page read the
// library through it.  Such a request still says whose name it was sent
// to, which is how it's caught.
func OnlyHosts(names []string, next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, name := range names {
		if u, err := url.Parse(name); err == nil && u.Host != "" {
			name = u.Hostname()
		}
		if name != "" {
			allowed[strings.ToLower(strings.TrimSuffix(name, "."))] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
		if _, err := netip.ParseAddr(host); err != nil && !allowed[host] {
			http.Error(w, "this server isn't "+host+"; if it should be, give its URL as --base-url", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}
