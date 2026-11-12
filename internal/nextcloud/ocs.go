package nextcloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ocs calls an OCS endpoint and decodes the "data" member of its answer into out.
// path starts with "/ocs/"; form is sent as a URL-encoded body when not nil.
func (c *Client) ocs(ctx context.Context, method, path string, form url.Values, out any) error {
	headers := map[string]string{"OCS-APIRequest": "true", "Accept": "application/json"}
	var body []byte
	if form != nil {
		body = []byte(form.Encode())
		headers["Content-Type"] = "application/x-www-form-urlencoded"
	}
	resp, err := c.do(ctx, method, c.base+path, body, headers)
	if err != nil {
		return err
	}
	defer drain(resp)
	if resp.StatusCode == http.StatusNotModified {
		return nil // "nothing new", e.g. an empty activity stream
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return Errorf("Could not read the answer from Nextcloud.")
	}

	var envelope struct {
		OCS struct {
			Meta struct {
				StatusCode int    `json:"statuscode"`
				Message    string `json:"message"`
			} `json:"meta"`
			Data json.RawMessage `json:"data"`
		} `json:"ocs"`
	}
	decoded := json.Unmarshal(data, &envelope) == nil
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Prefer the reason Nextcloud gives (e.g. "Password needs to be at least 10 characters").
		if message := strings.TrimSpace(envelope.OCS.Meta.Message); decoded && message != "" && message != "OK" {
			return &Error{Message: "Nextcloud refused the request: " + c.Scrub(message), Status: resp.StatusCode}
		}
		return statusError(resp)
	}
	if !decoded {
		return Errorf("Nextcloud returned a response that could not be understood.")
	}
	if out == nil || len(envelope.OCS.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(envelope.OCS.Data, out); err != nil {
		return Errorf("Nextcloud returned a response that could not be understood.")
	}
	return nil
}

// Capabilities is the part of the server capabilities the tools rely on.
type Capabilities struct {
	// Version is the Nextcloud version, e.g. "33.0.10".
	Version string
	// Trash is true when deleted files go to the trash bin.
	Trash bool
	// Versioning is true when file versions are kept.
	Versioning bool
	// Apps lists the capability sections present (roughly, the enabled apps).
	Apps []string
}

type capabilityCache struct {
	mu      sync.Mutex
	value   Capabilities
	fetched time.Time
}

const capabilityTTL = 5 * time.Minute

// Capabilities asks Nextcloud what it supports. The answer is cached briefly.
func (c *Client) Capabilities(ctx context.Context) (Capabilities, error) {
	return c.capabilities(ctx, false)
}

// FreshCapabilities asks Nextcloud again, ignoring the cache. Use it before
// an action whose safety depends on the answer.
func (c *Client) FreshCapabilities(ctx context.Context) (Capabilities, error) {
	return c.capabilities(ctx, true)
}

func (c *Client) capabilities(ctx context.Context, fresh bool) (Capabilities, error) {
	c.caps.mu.Lock()
	defer c.caps.mu.Unlock()
	if !fresh && !c.caps.fetched.IsZero() && time.Since(c.caps.fetched) < capabilityTTL {
		return c.caps.value, nil
	}
	var raw struct {
		Version struct {
			String string `json:"string"`
		} `json:"version"`
		Capabilities map[string]json.RawMessage `json:"capabilities"`
	}
	if err := c.ocs(ctx, http.MethodGet, "/ocs/v2.php/cloud/capabilities", nil, &raw); err != nil {
		return Capabilities{}, err
	}
	var files struct {
		Undelete   bool `json:"undelete"`
		Versioning bool `json:"versioning"`
	}
	if section, ok := raw.Capabilities["files"]; ok {
		_ = json.Unmarshal(section, &files)
	}
	value := Capabilities{Version: raw.Version.String, Trash: files.Undelete, Versioning: files.Versioning}
	for name := range raw.Capabilities {
		value.Apps = append(value.Apps, name)
	}
	c.caps.value, c.caps.fetched = value, time.Now()
	return value, nil
}
