// Package ingest brings files into a library by way of its server: it is
// a JMAP client that uploads each file and calls Photo/import, like any
// other client could.  See doc/types.md for the methods it uses.
package ingest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// capability is the capability Photo is under.  It is the server
// package's Capability, written out so that a client needn't be built
// with a server in it.
const capability = "https://semiotic.systems/jmap/jiggle"

// A Client makes requests of one account on a jiggle server.
type Client struct {
	// BaseURL is where the server was found, without a trailing slash.
	BaseURL string

	http      *http.Client
	apiURL    string
	uploadURL string
	accountID string
}

// Connect reads the session of the server at base, like
// http://localhost:8739, and returns a client of its photos account.
func Connect(ctx context.Context, base string) (*Client, error) {
	c := &Client{BaseURL: strings.TrimRight(base, "/"), http: &http.Client{}}

	var session struct {
		APIURL          string            `json:"apiUrl"`
		UploadURL       string            `json:"uploadUrl"`
		PrimaryAccounts map[string]string `json:"primaryAccounts"`
	}
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/.well-known/jmap", nil)
	if err != nil {
		return nil, err
	}
	if err := c.do(req, &session); err != nil {
		return nil, fmt.Errorf("no jiggle server at %s: %w", c.BaseURL, err)
	}
	c.accountID = session.PrimaryAccounts[capability]
	if c.accountID == "" {
		return nil, fmt.Errorf("the server at %s has no account of photos", c.BaseURL)
	}
	c.apiURL = session.APIURL
	c.uploadURL = strings.ReplaceAll(session.UploadURL, "{accountId}", c.accountID)
	return c, nil
}

// do sends a request and decodes its answer, which must be a success, as
// JSON into out.
func (c *Client) do(req *http.Request, out any) error {
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 500))
		return fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(res.Body).Decode(out)
}

// A MethodError is a method-level error: the method wasn't done at all.
type MethodError struct {
	Method      string
	Type        string
	Description string
}

func (e *MethodError) Error() string {
	if e.Description == "" {
		return e.Method + ": " + e.Type
	}
	return e.Method + ": " + e.Type + ": " + e.Description
}

// Call makes one method call on the account, and decodes the arguments of
// its response into out.
func (c *Client) Call(ctx context.Context, method string, args map[string]any, out any) error {
	with := map[string]any{"accountId": c.accountID}
	for k, v := range args {
		with[k] = v
	}
	body, err := json.Marshal(map[string]any{
		"using":       []string{"urn:ietf:params:jmap:core", capability},
		"methodCalls": []any{[]any{method, with, "a"}},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	var answer struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := c.do(req, &answer); err != nil {
		return fmt.Errorf("%s: %w", method, err)
	}
	if len(answer.MethodResponses) != 1 || len(answer.MethodResponses[0]) != 3 {
		return fmt.Errorf("%s: the server's answer makes no sense", method)
	}
	var name string
	if err := json.Unmarshal(answer.MethodResponses[0][0], &name); err != nil {
		return err
	}
	if name == "error" {
		failed := &MethodError{Method: method}
		var e struct{ Type, Description string }
		json.Unmarshal(answer.MethodResponses[0][1], &e)
		failed.Type, failed.Description = e.Type, e.Description
		return failed
	}
	return json.Unmarshal(answer.MethodResponses[0][1], out)
}

// Upload uploads a file's content, of the given size, and returns the id
// of the blob it becomes.
func (c *Client) Upload(ctx context.Context, content io.Reader, size int64) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.uploadURL, content)
	if err != nil {
		return "", err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", "application/octet-stream")

	var uploaded struct {
		BlobID string `json:"blobId"`
	}
	if err := c.do(req, &uploaded); err != nil {
		return "", err
	}
	if uploaded.BlobID == "" {
		return "", errors.New("the server gave the upload no blobId")
	}
	return uploaded.BlobID, nil
}
