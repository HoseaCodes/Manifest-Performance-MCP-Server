package httpserver

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A structurally valid, unexpired delegated token. Not signed — nothing in this
// package checks signatures; the application API is the authority on that.
func usableTestToken() string {
	segment := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return segment(map[string]any{"alg": "RS256", "typ": "JWT"}) + "." +
		segment(map[string]any{
			"sub": "athlete-1",
			"act": map[string]any{"sub": "workout-mcp"},
			"exp": time.Now().Add(time.Hour).Unix(),
		}) + ".sig"
}

func testHandler() http.Handler {
	return Handler(Config{
		APIBaseURL:          "https://app.example.com",
		PublicURL:           "https://mcp.example.com",
		AuthorizationServer: "https://auth.example.com",
		Version:             "test",
	})
}

/*
TestUnauthenticatedIsRefusedWithDiscovery covers the half that is easy to get
half-right.

A bare 401 leaves an MCP client with nowhere to go. The WWW-Authenticate header
pointing at the resource metadata is what lets it find the authorization server
on its own, which is the difference between a connector that can be added by
someone clicking a button and one that needs manual configuration.
*/
func TestUnauthenticatedIsRefusedWithDiscovery(t *testing.T) {
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.Code)
	}

	challenge := res.Header().Get("WWW-Authenticate")
	if !strings.HasPrefix(challenge, "Bearer ") {
		t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", challenge)
	}
	if !strings.Contains(challenge, "/.well-known/oauth-protected-resource") {
		t.Errorf("WWW-Authenticate = %q, want it to point at the resource metadata", challenge)
	}
}

// A malformed Authorization header is not a credential.
func TestMalformedAuthorizationIsRefused(t *testing.T) {
	for _, header := range []string{"", "Basic abc", "Bearer", "Bearer ", "token abc"} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		res := httptest.NewRecorder()
		testHandler().ServeHTTP(res, req)

		if res.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status = %d, want 401", header, res.Code)
		}
	}
}

// Case-insensitive scheme, per RFC 7235. A client that sends "bearer" is not wrong.
func TestBearerSchemeIsCaseInsensitive(t *testing.T) {
	// A usable token, so this isolates the scheme casing rather than failing on
	// the token check that also runs here.
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "bearer "+usableTestToken())
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)

	if res.Code == http.StatusUnauthorized {
		t.Error("a lowercase bearer scheme was refused")
	}
}

/*
TestResourceMetadata checks the document a client reads to find the
authorization server.

`bearer_methods_supported` is header-only deliberately: a token in a query
string lands in access logs, proxy logs and Referer headers, and advertising
that method invites clients to use it.
*/
func TestResourceMetadata(t *testing.T) {
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, httptest.NewRequest(
		http.MethodGet, "/.well-known/oauth-protected-resource", nil))

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}

	var doc struct {
		Resource               string   `json:"resource"`
		AuthorizationServers   []string `json:"authorization_servers"`
		BearerMethodsSupported []string `json:"bearer_methods_supported"`
		ScopesSupported        []string `json:"scopes_supported"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if doc.Resource != "https://mcp.example.com" {
		t.Errorf("resource = %q", doc.Resource)
	}
	if len(doc.AuthorizationServers) != 1 || doc.AuthorizationServers[0] != "https://auth.example.com" {
		t.Errorf("authorization_servers = %v", doc.AuthorizationServers)
	}
	for _, method := range doc.BearerMethodsSupported {
		if method != "header" {
			t.Errorf("bearer_methods_supported includes %q; a token in a URL is logged everywhere", method)
		}
	}
	if len(doc.ScopesSupported) != 3 {
		t.Errorf("scopes_supported = %v, want the three the tools need", doc.ScopesSupported)
	}
}

// Discovery and health must be reachable without a credential, or a client can
// never learn how to obtain one.
func TestDiscoveryAndHealthNeedNoToken(t *testing.T) {
	for _, path := range []string{"/.well-known/oauth-protected-resource", "/healthz"} {
		res := httptest.NewRecorder()
		testHandler().ServeHTTP(res, httptest.NewRequest(http.MethodGet, path, nil))
		if res.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, res.Code)
		}
	}
}

/*
TestNoAmbientCredential is the property that separates this from the stdio mode.

A remote server serves whoever calls it. If a token could come from anywhere
other than the request — an environment variable, a cached client, a default —
then every caller would act as whichever athlete that token belongs to. It would
work flawlessly with one user and be a cross-athlete breach with two.
*/
func TestNoAmbientCredential(t *testing.T) {
	t.Setenv("MANIFEST_SERVICE_TOKEN", usableTestToken())

	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — an environment token must not authenticate a caller", res.Code)
	}
}

/*
TestExpiredOrNonDelegatedTokensAreRefusedAtTheHandshake covers the failure that
has no recovery path.

An unusable token used to complete `initialize` and only fail on the first tool
call, as an API error inside a tool result. An MCP client cannot see a 401
there, so it never learns to re-authorize — the athlete's connector simply stops
working, with no signal saying why or what to do.

These are refused at the HTTP layer, where the 401 and its WWW-Authenticate
challenge send the client back through the authorization flow.
*/
func TestExpiredOrNonDelegatedTokensAreRefusedAtTheHandshake(t *testing.T) {
	segment := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	head := segment(map[string]any{"alg": "RS256", "typ": "JWT"})
	jwt := func(claims map[string]any) string {
		return head + "." + segment(claims) + ".sig"
	}

	future := time.Now().Add(time.Hour).Unix()
	past := time.Now().Add(-time.Hour).Unix()
	delegated := map[string]any{"sub": "athlete", "act": map[string]any{"sub": "workout-mcp"}}

	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"not a jwt", "not-a-real-token", http.StatusUnauthorized},
		{"two segments", head + ".abc", http.StatusUnauthorized},
		{"undecodable payload", head + ".!!!.sig", http.StatusUnauthorized},
		{"expired", jwt(map[string]any{"sub": "a", "act": map[string]any{"sub": "m"}, "exp": past}), http.StatusUnauthorized},
		{"no exp", jwt(delegated), http.StatusUnauthorized},
		{"user token, not delegated", jwt(map[string]any{"sub": "a", "exp": future}), http.StatusUnauthorized},
		{"current delegated token", jwt(map[string]any{"sub": "a", "act": map[string]any{"sub": "m"}, "exp": future}), http.StatusOK},
	}

	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		res := httptest.NewRecorder()
		testHandler().ServeHTTP(res, req)

		if tc.want == http.StatusUnauthorized && res.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", tc.name, res.Code)
		}
		if tc.want == http.StatusOK && res.Code == http.StatusUnauthorized {
			t.Errorf("%s: refused a token that should start a session", tc.name)
		}
	}
}

/*
TestPreflightIsNotAuthenticated covers a failure with no visible symptom.

A browser sends OPTIONS before a cross-origin request carrying an Authorization
header, and deliberately sends it *without* that header. Answering 401 fails the
preflight, so the real request is never made: the client reports only that the
connection could not be set up, and the server logs nothing at all, because
nothing arrived. Both sides look fine in isolation.
*/
func TestPreflightIsNotAuthenticated(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	req.Header.Set("Origin", "https://chatgpt.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")

	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)

	if res.Code == http.StatusUnauthorized {
		t.Fatal("preflight was refused for want of a credential it cannot carry")
	}
	if res.Code != http.StatusNoContent && res.Code != http.StatusOK {
		t.Errorf("status = %d, want 204", res.Code)
	}

	allowed := res.Header().Get("access-control-allow-headers")
	if !strings.Contains(strings.ToLower(allowed), "authorization") {
		t.Errorf("allow-headers = %q, want it to permit authorization", allowed)
	}
}

// The credential is still required on the real request; only the preflight is exempt.
func TestPreflightExemptionDoesNotWeakenTheRealRequest(t *testing.T) {
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/mcp", nil))

	if res.Code != http.StatusUnauthorized {
		t.Fatalf("POST status = %d, want 401", res.Code)
	}
}

// A header the browser cannot read is a header the client does not have.
func TestSessionIdIsExposedToTheBrowser(t *testing.T) {
	req := httptest.NewRequest(http.MethodOptions, "/mcp", nil)
	res := httptest.NewRecorder()
	testHandler().ServeHTTP(res, req)

	exposed := strings.ToLower(res.Header().Get("access-control-expose-headers"))
	for _, want := range []string{"mcp-session-id", "www-authenticate"} {
		if !strings.Contains(exposed, want) {
			t.Errorf("expose-headers = %q, want it to include %q", exposed, want)
		}
	}
}
