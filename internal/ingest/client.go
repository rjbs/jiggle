// Package ingest brings files into a library by way of its server: it is
// a JMAP client that uploads each file and calls Photo/import, like any
// other client could.  See doc/types.md for the methods it uses.
package ingest

import (
	"context"
	"fmt"
	"strings"

	"github.com/rjbs/gizmo/client"
)

// capability is the capability Photo is under.  It is the server
// package's Capability, written out so that a client needn't be built
// with a server in it.
const capability = "https://semiotic.systems/jmap/jiggle"

// A Client makes requests of one account on a jiggle server.  It is
// Gizmo's JMAP client.
type Client = client.Client

// Connect reads the session of the server at base, like
// http://localhost:8739, and returns a client of its photos account.
func Connect(ctx context.Context, base string) (*Client, error) {
	c := client.New(strings.TrimRight(base, "/"), "urn:ietf:params:jmap:core", capability)
	if _, err := c.Session(ctx); err != nil {
		return nil, fmt.Errorf("no jiggle server at %s: %w", c.BaseURL, err)
	}
	if err := c.UseAccount(ctx, capability); err != nil {
		return nil, fmt.Errorf("the server at %s has no account of photos", c.BaseURL)
	}
	return c, nil
}
