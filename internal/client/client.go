// Package client is the only way this server reaches data.
//
// There is no database driver here and no connection string. That is the whole
// point of the package boundary: the restriction is a property of the Go module
// graph rather than a rule this file promises to follow.
package client

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

const defaultTimeout = 15 * time.Second

// APIError carries enough for an assistant to recover rather than just retry.
type APIError struct {
	Status int
	// Message as the API reported it, never an internal detail.
	Message string
	// Errors is the per-field detail a 422 carries.
	Errors []FieldError
	// RequestID ties a failure an assistant reports to one set of server log
	// lines. Without it, "it didn't work" is unresolvable.
	RequestID string
}

type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("request failed with status %d", e.Status)
}

type errorBody struct {
	Error  string       `json:"error"`
	Errors []FieldError `json:"errors"`
}

// Client talks to the manifestfitness internal API.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New returns a client, or an error when configuration is missing.
//
// Failing here rather than at first call means a misconfigured server refuses
// to start instead of appearing healthy and failing on the athlete's first
// request.
func New(baseURL, token string) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, fmt.Errorf("MANIFEST_API_BASE_URL is not set")
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("MANIFEST_SERVICE_TOKEN is not set")
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: defaultTimeout},
	}, nil
}

// WithHTTPClient replaces the transport. Used by tests to point at a stub.
func (c *Client) WithHTTPClient(h *http.Client) *Client {
	c.http = h
	return c
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, string, error) {
	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, "", fmt.Errorf("encoding request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, "", err
	}

	// The token names both parties: `sub` is the athlete, `act.sub` is this
	// service. It is never constructed here — this server holds no signing key.
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}

	requestID := resp.Header.Get("x-request-id")

	if resp.StatusCode >= 400 {
		var parsed errorBody
		_ = json.Unmarshal(raw, &parsed)
		return nil, requestID, &APIError{
			Status:    resp.StatusCode,
			Message:   parsed.Error,
			Errors:    parsed.Errors,
			RequestID: requestID,
		}
	}

	return raw, requestID, nil
}

// TrainingContext fetches everything a generation needs, as one hashable object.
func (c *Client) TrainingContext(ctx context.Context, query url.Values) (json.RawMessage, string, error) {
	return c.do(ctx, http.MethodGet, "/api/internal/me/training-context", query, nil)
}

// CreatePrescription creates one prescribed session.
func (c *Client) CreatePrescription(ctx context.Context, body any) (json.RawMessage, string, error) {
	return c.do(ctx, http.MethodPost, "/api/internal/me/workout-prescriptions", nil, body)
}

// GetPrescription reads one back, scoped to the token's athlete.
func (c *Client) GetPrescription(ctx context.Context, id string) (json.RawMessage, string, error) {
	return c.do(ctx, http.MethodGet, "/api/internal/me/workout-prescriptions/"+url.PathEscape(id), nil, nil)
}

// Supersede replaces an unstarted session. Addressed to the slot, not the
// prescription: prescriptions are immutable and the slot owns the pointer.
func (c *Client) Supersede(ctx context.Context, slotID string, body any) (json.RawMessage, string, error) {
	return c.do(ctx, http.MethodPost,
		"/api/internal/me/workout-slots/"+url.PathEscape(slotID)+"/supersede", nil, body)
}

// Note what is absent: no method records completion, readiness, or a deletion.
// Not because they are forbidden here, but because the endpoints they would
// call do not exist on this surface.
