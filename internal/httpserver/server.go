/*
Package httpserver exposes the MCP tools over HTTP, for remote clients such as
ChatGPT that cannot launch a local process.

The difference from stdio is not the transport. It is **who the token belongs
to**.

A stdio server is launched by one person, holds one athlete's token in an
environment variable, and serves exactly them. A remote server is reachable by
anyone and serves whoever presents a credential — so the token must come from
the request, never from configuration. A hosted server with a token in its
environment would act for that one athlete no matter who called it, which is the
worst version of this mistake: it would work perfectly in testing.

So each request builds its own API client from its own bearer token. Nothing
about an athlete is held between requests, and there is no ambient credential to
fall back on.

## Authorization

This server is a *resource* server. It issues nothing and verifies nothing
locally: the token is passed to the application API, which verifies it against
Storm Gate's JWKS and decides what it permits. Adding a second verifier here
would be a second place for the answer to be wrong.

`/.well-known/oauth-protected-resource` (RFC 9728) tells a client which
authorization server to go to, and a 401 carries `WWW-Authenticate` pointing at
it. That pair is how an MCP client discovers where to authenticate without being
told out of band.
*/
package httpserver

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hoseacodes/manifestfitness/workout-mcp/internal/client"
	"github.com/hoseacodes/manifestfitness/workout-mcp/internal/tools"
)

// serverVersion is reported by both the SDK handshake and the discover probe.
var serverVersion = "0.2.0"

type Config struct {
	// APIBaseURL is the application API this proxies to.
	APIBaseURL string
	// PublicURL is this server's own canonical URL, and its resource identifier.
	PublicURL string
	// AuthorizationServer is Storm Gate's issuer URL.
	AuthorizationServer string
	// Version reported to clients.
	Version string

	/*
	 * Stateless runs without server-side session state.
	 *
	 * Required on Lambda, and the reason this option exists: the SDK keeps
	 * sessions in a map in process memory, and each Lambda invocation may land
	 * in a different container. A session established on one would be unknown
	 * to the next, so a client would be told its session id is invalid partway
	 * through a conversation — intermittently, and more often under load, which
	 * is the worst way to find out.
	 *
	 * The cost is that the server cannot initiate requests to the client. These
	 * tools never do: every one is a call and a reply.
	 */
	Stateless bool
}

func bearerFrom(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if header == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

/*
usable reports whether a token is worth starting a session with.

Deliberately **not** verification. The signature is not checked, and no
authorization decision is made here — the application API verifies against Storm
Gate's JWKS and decides what the token permits, and a second verifier would be a
second place for that answer to be wrong.

What this catches is the common case: a token that is expired, malformed, or not
a delegated token at all. Without it those complete the handshake and fail later
as an API error inside a tool result, where an MCP client cannot see a 401 and
therefore never learns to re-authorize. The athlete's session simply stops
working with no way back.

A well-formed but forged token still passes here and is refused by the API,
which is the correct division: this decides whether to *start*, the API decides
what is *allowed*.
*/
func usable(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}

	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return false
	}

	var claims struct {
		Exp int64 `json:"exp"`
		Act *struct {
			Sub string `json:"sub"`
		} `json:"act"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return false
	}

	// `act` is what makes it a delegated token. A plain user token would be
	// refused by the internal API anyway; failing here says so at a point the
	// client can act on.
	if claims.Act == nil || claims.Act.Sub == "" {
		return false
	}

	// A minute of slack, matching the API's own tolerance, so a token that is
	// about to be accepted there is not rejected here.
	return claims.Exp != 0 && time.Now().Add(-time.Minute).Unix() < claims.Exp
}

// Handler returns the full HTTP surface: the MCP endpoint, the protected
// resource metadata, and a health check.
func Handler(cfg Config) http.Handler {
	mux := http.NewServeMux()

	/*
	 * RFC 9728. A client that receives a 401 from the MCP endpoint reads this to
	 * find out where to authenticate, rather than needing it configured.
	 *
	 * `bearer_methods_supported` is header-only on purpose: a token in a query
	 * string lands in access logs, proxy logs and Referer headers.
	 */
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		writeCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"resource":                 cfg.PublicURL,
			"authorization_servers":    []string{cfg.AuthorizationServer},
			"bearer_methods_supported": []string{"header"},
			"scopes_supported": []string{
				"training:read", "workouts:read", "workouts:write",
			},
		})
	})

	// Liveness only. It deliberately does not check the application API: a
	// dependency being down is not this process being unhealthy, and conflating
	// them makes a restart look like a fix.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	})

	/*
	 * A server per request, built from that request's token.
	 *
	 * `getServer` receives the *http.Request, which is the only reason this can
	 * be per-athlete at all. The tools are registered against a client that
	 * holds this caller's credential and nothing else.
	 */
	mcpOptions := &mcp.StreamableHTTPOptions{
		Stateless: cfg.Stateless,
		// Plain JSON rather than an event stream when stateless. API Gateway
		// buffers responses, so a stream would be held until complete and
		// arrive as one chunk anyway — the appearance of streaming without any
		// of it.
		JSONResponse: cfg.Stateless,
	}

	mcpHandler := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		api, err := client.New(cfg.APIBaseURL, bearerFrom(r))
		if err != nil {
			// Unreachable: the middleware below rejects a missing token before
			// this runs. Returning nil rather than a server with no credential
			// keeps that true even if the middleware is ever reordered.
			return nil
		}

		serverVersion = cfg.Version
		server := mcp.NewServer(&mcp.Implementation{
			Name:    "workout-mcp",
			Version: cfg.Version,
		}, nil)
		tools.Register(server, api)
		return server
	}, mcpOptions)

	// Order matters: the discover shim sits *inside* requireBearer, so a probe
	// still needs a credential. Answering it earlier — where the body is
	// already buffered for logging — was a hole around the bearer check, and a
	// test caught it.
	handler := logRequests(requireBearer(cfg, discoverShim(mcpHandler)))
	mux.Handle("/mcp", handler)
	mux.Handle("/mcp/", handler)

	/*
	 * A second endpoint exposing exactly one trivial tool — see minimal.go.
	 *
	 * Diagnostic, not part of the product surface. It exists to separate "this
	 * client cannot bind any tool" from "this client rejects something in these
	 * schemas", which the real endpoint cannot distinguish on its own.
	 */
	minimalHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return minimalServer(cfg.Version)
	}, mcpOptions)
	minimal := logRequests(requireBearer(cfg, discoverShim(minimalHandler)))
	mux.Handle("/mcp-min", minimal)
	mux.Handle("/mcp-min/", minimal)

	return mux
}

/*
requireBearer refuses an unauthenticated request and says where to authenticate.

The `WWW-Authenticate` header is the half that matters. Without it a client sees
a bare 401 and has nowhere to go; with it, it discovers the authorization server
from the metadata document and can run the flow unattended.

The token's signature is not checked here, and no authorization decision is made
— see `usable`. What is checked is whether it is worth starting a session with
at all, so an expired credential produces a 401 the client can act on rather
than an error buried in a tool result.
*/
func requireBearer(cfg Config, next http.Handler) http.Handler {
	challenge := `Bearer resource_metadata="` + strings.TrimRight(cfg.PublicURL, "/") +
		`/.well-known/oauth-protected-resource"`

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		/*
		 * A preflight is not a request for anything, and carries no credential.
		 *
		 * Browsers send OPTIONS before a cross-origin call with an Authorization
		 * header, deliberately without that header. Answering 401 fails the
		 * preflight, so the real request is never sent — the connection simply
		 * cannot be established, and nothing reaches the server to explain why.
		 * That is what this looked like from the outside: silence.
		 */
		if r.Method == http.MethodOptions {
			writeCORS(w)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		writeCORS(w)

		token := bearerFrom(r)
		if token == "" || !usable(token) {
			w.Header().Set("WWW-Authenticate", challenge)
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error":             "unauthorized",
				"error_description": "A current delegated access token is required. See the resource metadata for the authorization server.",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

/*
logRequests records which JSON-RPC method each call carries.

Added after a client connected, called nothing, and wrote a session from its own
memory instead — and there was no way to tell from this side whether it had
asked for the tools at all. "It didn't use the tools" and "it never saw them"
look identical without this, and they have opposite fixes.

The body is read and replaced rather than consumed, so the handler still sees
it. Only the method name is logged: the arguments are an athlete's training
data, and this is not the place for it.
*/
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method := ""
		if r.Body != nil && r.Method == http.MethodPost {
			// Bounded: a malformed or hostile body must not be buffered whole
			// just to name it.
			raw, err := io.ReadAll(io.LimitReader(r.Body, 64*1024))
			_ = r.Body.Close()
			if err == nil {
				var probe struct {
					Method string `json:"method"`
				}
				_ = json.Unmarshal(raw, &probe)
				method = probe.Method

				/*
				 * When there is no `method`, describe the shape rather than the
				 * content. A batch arrives as an array and a response carries
				 * `result` or `error`; both logged as a bare "POST", which said
				 * only that something arrived, not what it was.
				 *
				 * Keys, never values: tool arguments are an athlete's training
				 * data, and a log is the wrong place for it.
				 */
				if method == "" {
					var shape any
					if json.Unmarshal(raw, &shape) == nil {
						switch node := shape.(type) {
						case []any:
							method = fmt.Sprintf("batch[%d]", len(node))
						case map[string]any:
							keys := make([]string, 0, len(node))
							for k := range node {
								keys = append(keys, k)
							}
							sort.Strings(keys)
							method = "object{" + strings.Join(keys, ",") + "}"
						}
					}
				}
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}

		if method == "" {
			method = r.Method
		}
		// Path included: the real surface and the minimal diagnostic one are
		// both mounted, and "tools/list succeeded" means nothing without
		// knowing which of them answered.
		log.Printf("mcp %s %s", r.URL.Path, method)
		next.ServeHTTP(w, r)
	})
}

/*
writeCORS permits the browser-side calls a remote MCP client makes.

`Mcp-Session-Id` is both accepted and exposed: a stateful server returns it on
initialize and the client must read it back, and a header a browser cannot read
is a header the client does not have.
*/
func writeCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("access-control-allow-origin", "*")
	h.Set("access-control-allow-methods", "GET, POST, DELETE, OPTIONS")
	h.Set("access-control-allow-headers",
		"authorization, content-type, accept, mcp-session-id, mcp-protocol-version, last-event-id")
	h.Set("access-control-expose-headers", "mcp-session-id, www-authenticate")
	h.Set("access-control-max-age", "86400")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("workout-mcp: writing response: %v", err)
	}
}
