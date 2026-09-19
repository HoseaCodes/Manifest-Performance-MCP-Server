package httpserver

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
)

/*
SEP-2575 `server/discover`, answered here rather than by the SDK.

The Go SDK v1.8.0 has a handler for this, but its streamable HTTP transport
rejects the method before dispatch, so the server replies `-32601 method not
found`. That is a legal answer — the SDK's own client tests expect it — and a
client is supposed to fall back to the legacy `initialize` handshake when it
sees one.

ChatGPT does not fall back. It calls `server/discover`, receives the error, and
reports that the connector exposes no tools. `tools/list` is never sent: the
tools exist, are valid, and are never asked for.

So this answers the probe instead of refusing it, and answers it **honestly** —
advertising only the protocol versions this server actually speaks. That is what
makes it safe: per SEP-2575, a client seeing versions older than 2026-07-28
falls back to `initialize`, which is the path that works. Claiming the newer
version to satisfy the probe would trade a visible failure for a subtler one,
where the client proceeds under a protocol the transport cannot serve.
*/

// Versions this server genuinely serves, newest first. Deliberately excludes
// 2026-07-28: the SDK's transport does not implement the stateless protocol,
// and saying otherwise would be a claim we cannot honour.
var servedProtocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

type jsonrpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Method  string `json:"method"`
}

/*
answerDiscover replies to a `server/discover` probe, or reports that it did not.

Returns false for anything else, so the request continues to the SDK untouched.
*/
func answerDiscover(w http.ResponseWriter, body []byte, accept string) bool {
	var req jsonrpcRequest
	if err := json.Unmarshal(body, &req); err != nil || req.Method != "server/discover" {
		return false
	}
	// A notification has no id and expects no reply.
	if req.ID == nil {
		return false
	}

	result := map[string]any{
		"supportedVersions": servedProtocolVersions,
		"capabilities": map[string]any{
			"tools":   map[string]any{"listChanged": true},
			"logging": map[string]any{},
		},
		"serverInfo": map[string]any{
			"name":    "workout-mcp",
			"version": serverVersion,
		},
	}

	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      req.ID,
		"result":  result,
	})
	if err != nil {
		return false
	}

	/*
	 * Matched to what the caller accepts. The streamable transport permits a
	 * plain JSON reply to a POST, but a client that asked for an event stream
	 * and is handed bare JSON may not read it — and this whole problem has been
	 * one client quietly declining to continue.
	 */
	if strings.Contains(accept, "text/event-stream") {
		w.Header().Set("content-type", "text/event-stream")
		w.Header().Set("cache-control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message\ndata: "))
		_, _ = w.Write(payload)
		_, _ = w.Write([]byte("\n\n"))
		return true
	}

	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
	return true
}

/*
discoverShim answers a `server/discover` probe and passes everything else on.

Mounted behind the bearer check, not in front of it: a probe reveals the
server's capabilities, and there is no reason for that to be the one thing an
unauthenticated caller can read.
*/
func discoverShim(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Body == nil {
			next.ServeHTTP(w, r)
			return
		}

		raw, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
		_ = r.Body.Close()
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		// Restored either way, so the SDK still sees an unread body.
		r.Body = io.NopCloser(bytes.NewReader(raw))

		if answerDiscover(w, raw, r.Header.Get("Accept")) {
			log.Printf("mcp %s server/discover (answered locally)", r.URL.Path)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Unwired — see the note in Handler. Kept so the next attempt does not have
// to rewrite it.
var _ = discoverShim
