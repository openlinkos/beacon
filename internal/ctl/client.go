package ctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"time"
)

// Client talks to the running service over its socket.
type Client struct{ hc *http.Client }

// Dial connects to the socket at path and checks that the service answers.
func Dial(ctx context.Context, path string) (*Client, Info, error) {
	c := newClient(func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	})
	var info Info
	if err := c.do(ctx, http.MethodGet, "/v1/info", nil, &info); err != nil {
		return nil, Info{}, err
	}
	return c, info, nil
}

func newClient(dial func(context.Context) (net.Conn, error)) *Client {
	return &Client{hc: &http.Client{
		Timeout: 30 * time.Second, // adding a machine waits for its host key
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
		},
	}}
}

// CurrentUser is the name recorded in the audit log for commands run from this process.
func CurrentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	if n := os.Getenv("USER"); n != "" {
		return n
	}
	return "unknown"
}

// CreateToken creates a token.
func (c *Client) CreateToken(ctx context.Context, in TokenRequest) (TokenResponse, error) {
	var out TokenResponse
	err := c.do(ctx, http.MethodPost, "/v1/tokens", in, &out)
	return out, err
}

// Enroll gives a machine its gateway token.
func (c *Client) Enroll(ctx context.Context, in EnrollRequest) (TokenResponse, error) {
	var out TokenResponse
	err := c.do(ctx, http.MethodPost, "/v1/enroll", in, &out)
	return out, err
}

// Tokens lists the tokens.
func (c *Client) Tokens(ctx context.Context) ([]TokenInfo, error) {
	var out []TokenInfo
	err := c.do(ctx, http.MethodGet, "/v1/tokens", nil, &out)
	return out, err
}

// TokenInfo is a token as the socket lists it (the hash is not interesting to people): SSHKeys are the
// fingerprints of its keys for the SSH channel, Machines the machines it reaches there (none: all).
// Lifetime is what it was created with, as token create -expires takes it; Renewed the last time it
// renewed itself.
type TokenInfo struct {
	Name      string    `json:"name"`
	Scopes    []string  `json:"scopes"`
	Subject   string    `json:"subject"`
	Created   time.Time `json:"created"`
	CreatedBy string    `json:"created_by"`
	Expires   time.Time `json:"expires"`
	SSHKeys   []string  `json:"ssh_keys,omitempty"`
	Machines  []string  `json:"machines,omitempty"`
	Lifetime  string    `json:"lifetime,omitempty"`
	Renewed   time.Time `json:"renewed,omitzero"`
}

func urlQuery(s string) string { return url.QueryEscape(s) }

// RevokeToken deletes a token.
func (c *Client) RevokeToken(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/v1/tokens/"+name, nil, nil)
}

// Drill starts or ends ("off") a drill.
func (c *Client) Drill(ctx context.Context, duration string) (DrillState, error) {
	var out DrillState
	err := c.do(ctx, http.MethodPost, "/v1/drill", DrillRequest{Duration: duration}, &out)
	return out, err
}

// DrillStatus reports the drill.
func (c *Client) DrillStatus(ctx context.Context) (DrillState, error) {
	var out DrillState
	err := c.do(ctx, http.MethodGet, "/v1/drill", nil, &out)
	return out, err
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://linkos-beacon"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set(UserHeader, CurrentUser())
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, MaxImport)) // an export is as large as an import may be
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			return errors.New(e.Error)
		}
		return fmt.Errorf("%s", resp.Status)
	}
	if out != nil && len(b) > 0 {
		return json.Unmarshal(b, out)
	}
	return nil
}
