package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// QuiClient is a thin HTTP client for Qui's automations API.
type QuiClient struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

func NewQuiClient(baseURL, apiKey string) *QuiClient {
	baseURL = strings.TrimRight(baseURL, "/")
	return &QuiClient{
		baseURL: baseURL,
		apiKey:  apiKey,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// QuiInstance is the minimal shape we care about from Qui's /api/instances.
// Qui returns more fields (host, connection status, hardlink settings, …);
// we ignore them — only id+name matter for display and mapping.
type QuiInstance struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// ListInstances fetches all Qui instances (id + human-friendly name).
// Used to label our export instances in the UI.
func (c *QuiClient) ListInstances(ctx context.Context) ([]QuiInstance, error) {
	body, err := c.do(ctx, "GET", c.baseURL+"/api/instances", nil)
	if err != nil {
		return nil, err
	}
	var out []QuiInstance
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode instances: %w (body: %s)", err, truncate(body, 200))
	}
	return out, nil
}

// Automation is a permissive representation. Top-level fields are typed for
// identity handling; the full payload is retained in Raw for roundtrip safety.
type Automation struct {
	ID             int             `json:"id"`
	InstanceID     int             `json:"instanceId"`
	Name           string          `json:"name"`
	TrackerPattern string          `json:"trackerPattern,omitempty"`
	Enabled        bool            `json:"enabled"`
	DryRun         bool            `json:"dryRun,omitempty"`
	Notify         bool            `json:"notify,omitempty"`
	SortOrder      int             `json:"sortOrder"`
	Raw            json.RawMessage `json:"-"`
}

// UnmarshalJSON stores the raw payload in addition to the typed fields.
func (a *Automation) UnmarshalJSON(data []byte) error {
	type aux Automation
	var tmp aux
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	*a = Automation(tmp)
	// json.Unmarshal may reuse the input buffer — copy to own it.
	a.Raw = append([]byte(nil), data...)
	return nil
}

// GetAutomation fetches a single automation by ID. Qui's API does not
// expose a per-rule GET — only a list GET and per-rule PUT/DELETE —
// so this helper fetches the full list and picks the matching entry.
// For apply loops that need many lookups, prefer calling
// ListAutomations once and indexing by ID; this wrapper is a
// convenience for single-rule callers.
func (c *QuiClient) GetAutomation(ctx context.Context, instanceID, ruleID int) (json.RawMessage, error) {
	list, err := c.ListAutomations(ctx, instanceID)
	if err != nil {
		return nil, err
	}
	for _, a := range list {
		if a.ID == ruleID {
			return a.Raw, nil
		}
	}
	return nil, fmt.Errorf("rule #%d not found in instance %d", ruleID, instanceID)
}

// CreateAutomation POSTs a new rule to Qui. The payload should omit
// id, instanceId, createdAt, updatedAt — Qui assigns those. Returns the
// server-assigned rule ID.
func (c *QuiClient) CreateAutomation(ctx context.Context, instanceID int, payload json.RawMessage) (int, error) {
	u := fmt.Sprintf("%s/api/instances/%d/automations", c.baseURL, instanceID)
	body, err := c.do(ctx, "POST", u, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	var resp struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, fmt.Errorf("decode create response: %w — %s", err, truncate(body, 200))
	}
	if resp.ID == 0 {
		return 0, fmt.Errorf("unexpected create response: %s", truncate(body, 200))
	}
	return resp.ID, nil
}

// UpdateAutomation PUTs a full rule payload to replace the existing
// rule's content. Payload should include id or omit it — Qui ignores id
// in the body since the URL path pins it.
func (c *QuiClient) UpdateAutomation(ctx context.Context, instanceID, ruleID int, payload json.RawMessage) error {
	u := fmt.Sprintf("%s/api/instances/%d/automations/%d", c.baseURL, instanceID, ruleID)
	_, err := c.do(ctx, "PUT", u, bytes.NewReader(payload))
	return err
}

// DeleteAutomation removes a rule from Qui. Used by orphan-delete in v0.3;
// kept here so the API surface is complete.
func (c *QuiClient) DeleteAutomation(ctx context.Context, instanceID, ruleID int) error {
	u := fmt.Sprintf("%s/api/instances/%d/automations/%d", c.baseURL, instanceID, ruleID)
	_, err := c.do(ctx, "DELETE", u, nil)
	return err
}

// Deactivate flips a rule's enabled flag to false. Used by Subscribe-side
// auto-sync when the maintainer removes a rule from the upstream repo —
// we deactivate (preserve, can be re-enabled manually) rather than delete
// (irreversible). The Qui rule and all its content stays intact.
//
// Qui's API has no PATCH endpoint and no single-rule GET — the only path
// is fetch-the-list, mutate the raw JSON, PUT back. The List call is
// already cached by callers (e.g. ApplySync passes liveByID), but this
// helper takes the round-trip itself so single-call sites stay simple.
// If a caller already holds the live raw JSON, it can construct the PUT
// directly via UpdateAutomation.
func (c *QuiClient) Deactivate(ctx context.Context, instanceID, ruleID int) error {
	rules, err := c.ListAutomations(ctx, instanceID)
	if err != nil {
		return fmt.Errorf("list automations: %w", err)
	}
	var raw json.RawMessage
	found := false
	for _, r := range rules {
		if r.ID == ruleID {
			raw = r.Raw
			found = true
			break
		}
	}
	if !found {
		// Already gone from Qui — the user deleted the rule themselves,
		// or it never existed. Treat as success: there is nothing left
		// to deactivate, which is the desired end-state anyway.
		return nil
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("decode rule %d: %w", ruleID, err)
	}
	if enabled, ok := data["enabled"].(bool); ok && !enabled {
		// Already disabled — no-op. Skip the round-trip.
		return nil
	}
	data["enabled"] = false
	payload, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("encode rule %d: %w", ruleID, err)
	}
	return c.UpdateAutomation(ctx, instanceID, ruleID, payload)
}

// ListAutomations fetches all automations for a given Qui instance.
func (c *QuiClient) ListAutomations(ctx context.Context, instanceID int) ([]Automation, error) {
	u := fmt.Sprintf("%s/api/instances/%d/automations", c.baseURL, instanceID)
	body, err := c.do(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	// Qui has historically returned either a bare array or a wrapped object.
	// Try array first, fall back to common wrapper shapes.
	var arr []Automation
	if err := json.Unmarshal(body, &arr); err == nil {
		return arr, nil
	}
	var wrapped struct {
		Automations []Automation `json:"automations"`
		Data        []Automation `json:"data"`
		Items       []Automation `json:"items"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return nil, fmt.Errorf("decode automations: %w (body: %s)", err, truncate(body, 200))
	}
	switch {
	case wrapped.Automations != nil:
		return wrapped.Automations, nil
	case wrapped.Data != nil:
		return wrapped.Data, nil
	case wrapped.Items != nil:
		return wrapped.Items, nil
	}
	return nil, fmt.Errorf("unexpected response shape: %s", truncate(body, 200))
}

// Ping does a cheap auth check by hitting /health (no auth) then /api/instances (auth).
func (c *QuiClient) Ping(ctx context.Context) error {
	if _, err := c.do(ctx, "GET", c.baseURL+"/health", nil); err != nil {
		return fmt.Errorf("qui /health: %w", err)
	}
	// /api/instances is the endpoint we will actually use — auth failure here
	// is what matters. 200 = ok; 401/403 = bad key.
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/api/instances", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("qui auth check: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("qui auth failed (HTTP %d) — check api_key", resp.StatusCode)
	}
	return nil
}

func (c *QuiClient) do(ctx context.Context, method, rawURL string, body io.Reader) ([]byte, error) {
	if _, err := url.Parse(rawURL); err != nil {
		return nil, fmt.Errorf("bad url: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s %s: HTTP %d — %s", method, rawURL, resp.StatusCode, truncate(data, 300))
	}
	return data, nil
}

func truncate(b []byte, n int) string {
	s := string(b)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
