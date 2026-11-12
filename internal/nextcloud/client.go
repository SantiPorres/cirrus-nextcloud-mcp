// Package nextcloud is a small client for the parts of the Nextcloud API the
// MCP tools need: WebDAV for files and, in later phases, OCS.
//
// Security properties enforced here, in one place:
//   - redirects are never followed;
//   - TLS verification is always on (there is no switch to disable it);
//   - credentials travel only in the Authorization header, and are scrubbed
//     from any error text;
//   - every path is confined to the configured root and may not contain "..";
//   - an existing file is only replaced with If-Match and its ETag.
package nextcloud

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SantiPorres/cirrus-nextcloud-mcp/internal/config"
)

// Error is a failure that is safe and useful to show to the MCP client.
type Error struct {
	Message string
	// Status is the HTTP status Nextcloud answered with, when that is what failed.
	Status int
}

func (e *Error) Error() string { return e.Message }

// IsNotFound reports whether err is Nextcloud saying the resource does not exist.
func IsNotFound(err error) bool {
	var typed *Error
	return errors.As(err, &typed) && typed.Status == http.StatusNotFound
}

// Errorf builds a user-facing Error.
func Errorf(format string, args ...any) *Error {
	return &Error{Message: fmt.Sprintf(format, args...)}
}

// Client talks to one Nextcloud account.
type Client struct {
	cfg  *config.Config
	http *http.Client
	// base is the Nextcloud URL without a trailing slash.
	base string
	// filesPrefix is the unescaped URL path of the user's WebDAV home.
	filesPrefix string
}

// New builds a client. The HTTP client never follows redirects and keeps no cookies.
func New(cfg *config.Config) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &Client{
		cfg: cfg,
		http: &http.Client{
			Transport: transport,
			Timeout:   2 * time.Minute,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		base:        strings.TrimRight(cfg.BaseURL.String(), "/"),
		filesPrefix: strings.TrimRight(cfg.BaseURL.Path, "/") + "/remote.php/dav/files/" + cfg.User,
	}
}

// Config returns the configuration the client was built with.
func (c *Client) Config() *config.Config { return c.cfg }

// Scrub removes the app password from a text, as a last line of defence
// before the text is logged or returned to the MCP client.
func (c *Client) Scrub(text string) string {
	secret := c.cfg.AppPassword.Reveal()
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "[redacted]")
}

// filesURL returns the WebDAV URL of an absolute path in the account.
func (c *Client) filesURL(abs string) string {
	return c.base + "/remote.php/dav/files/" + url.PathEscape(c.cfg.User) + escapePath(abs)
}

func escapePath(abs string) string {
	if abs == "" || abs == "/" {
		return ""
	}
	parts := strings.Split(strings.Trim(abs, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return "/" + strings.Join(parts, "/")
}

// do sends an authenticated request. The caller closes the response body.
func (c *Client) do(ctx context.Context, method, rawURL string, body []byte, headers map[string]string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, Errorf("Could not build the request to Nextcloud.")
	}
	req.SetBasicAuth(c.cfg.User, c.cfg.AppPassword.Reveal())
	req.Header.Set("User-Agent", "cirrus-nextcloud-mcp")
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// url.Error repeats the method and URL; keep only the cause.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return nil, Errorf("Could not reach Nextcloud: %s", c.Scrub(err.Error()))
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		drain(resp)
		return nil, Errorf("Nextcloud answered with a redirect (HTTP %d), which is never followed. Check NEXTCLOUD_URL.", resp.StatusCode)
	}
	return resp, nil
}

// drain discards and closes a response body so the connection can be reused.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}

// statusError maps the common failure statuses to user-facing errors. It
// returns nil for 2xx.
func statusError(resp *http.Response) error {
	switch code := resp.StatusCode; {
	case code >= 200 && code < 300:
		return nil
	case code == http.StatusUnauthorized:
		return Errorf("Nextcloud rejected the credentials.")
	case code == http.StatusForbidden:
		return Errorf("Nextcloud denied access to this resource. The account may not have permission.")
	case code == http.StatusNotFound:
		return &Error{Message: "Not found, or the Nextcloud user has no access to it.", Status: code}
	case code == http.StatusLocked:
		return Errorf("The file is locked (probably open in an editor). Try again later.")
	case code == http.StatusInsufficientStorage:
		return Errorf("Nextcloud has no storage left for this account.")
	default:
		return Errorf("Nextcloud answered with HTTP %d.", code)
	}
}
