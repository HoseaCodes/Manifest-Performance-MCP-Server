// Command workout-mcp exposes a restricted workout-prescription surface to MCP
// clients such as ChatGPT.
//
// Deliberately almost empty: everything that decides what may happen lives
// behind the API this talks to, and a server that made decisions of its own
// would be a second place to get them wrong.
//
// Two modes, and the difference between them is who the token belongs to:
//
//   - **stdio** (default) — launched by one person, serving one athlete, with
//     that athlete's token in the environment. This is how a desktop MCP client
//     runs it.
//   - **http** — reachable by anyone, serving whoever presents a credential, so
//     the token comes from each request and never from configuration. This is
//     what a remote client such as ChatGPT connects to.
//
// Running the http mode with a token in the environment would be the dangerous
// combination: it would act for that one athlete regardless of who called, and
// it would work perfectly in testing. The http path never reads that variable.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hoseacodes/manifestfitness/workout-mcp/internal/client"
	"github.com/hoseacodes/manifestfitness/workout-mcp/internal/httpserver"
	"github.com/hoseacodes/manifestfitness/workout-mcp/internal/tools"
)

const version = "0.2.0"

func main() {
	apiBaseURL := os.Getenv("MANIFEST_API_BASE_URL")

	switch strings.ToLower(os.Getenv("MCP_TRANSPORT")) {
	case "http":
		/*
		 * Stateless by default for a remote server.
		 *
		 * The SDK keeps sessions in process memory. That is fine for a process
		 * that stays up, and wrong for anything that can be restarted between a
		 * client's requests — Fly stops an idle machine, so a session created
		 * before the pause is unknown after it. The failure is intermittent and
		 * depends on how long the client waited, which is the hardest kind to
		 * attribute.
		 *
		 * These tools are request/response only and never initiate anything, so
		 * sessions buy nothing here. MCP_STATEFUL=true restores them for a
		 * deployment that genuinely stays warm.
		 */
		runHTTP(apiBaseURL, os.Getenv("MCP_STATEFUL") != "true")
	default:
		runStdio(apiBaseURL)
	}
}

func runStdio(apiBaseURL string) {
	// The token is obtained through the athlete's consent flow. This server
	// never mints one, and holds no signing key with which it could.
	api, err := client.New(apiBaseURL, os.Getenv("MANIFEST_SERVICE_TOKEN"))
	if err != nil {
		// Refuse to start rather than appear healthy and fail on the athlete's
		// first request.
		log.Fatalf("workout-mcp: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "workout-mcp",
		Version: version,
	}, nil)

	tools.Register(server, api)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("workout-mcp: %v", err)
	}
}

func configFromEnv(apiBaseURL string) httpserver.Config {
	return httpserver.Config{
		APIBaseURL:          apiBaseURL,
		PublicURL:           strings.TrimRight(os.Getenv("MCP_PUBLIC_URL"), "/"),
		AuthorizationServer: os.Getenv("STORM_GATE_ISSUER"),
		Version:             version,
	}
}

/*
mustBeServable refuses to start a remote server that cannot serve correctly.

Each of these is only read when a client is already mid-handshake, so a missing
value would otherwise surface as an unexplained failure during someone's first
connection attempt rather than as a boot error somebody sees.
*/
func mustBeServable(cfg httpserver.Config) {
	missing := []string{}
	if strings.TrimSpace(cfg.APIBaseURL) == "" {
		missing = append(missing, "MANIFEST_API_BASE_URL")
	}
	if strings.TrimSpace(cfg.PublicURL) == "" {
		missing = append(missing, "MCP_PUBLIC_URL")
	}
	if strings.TrimSpace(cfg.AuthorizationServer) == "" {
		missing = append(missing, "STORM_GATE_ISSUER")
	}
	if len(missing) > 0 {
		log.Fatalf("workout-mcp: remote mode requires %s", strings.Join(missing, ", "))
	}

	// Refused rather than ignored. A token in the environment cannot be used by
	// a remote mode, and an operator who set one believes it is doing something.
	if os.Getenv("MANIFEST_SERVICE_TOKEN") != "" {
		log.Fatalf("workout-mcp: MANIFEST_SERVICE_TOKEN must not be set in a remote mode — " +
			"it would imply one athlete's credential serves every caller, which it does not")
	}
}

func runHTTP(apiBaseURL string, stateless bool) {
	cfg := configFromEnv(apiBaseURL)
	cfg.Stateless = stateless
	mustBeServable(cfg)

	addr := os.Getenv("MCP_HTTP_ADDR")
	if addr == "" {
		// Hosts that inject a port expect it to be honoured.
		if port := os.Getenv("PORT"); port != "" {
			addr = ":" + port
		} else {
			addr = ":8080"
		}
	}

	server := &http.Server{
		Addr:    addr,
		Handler: httpserver.Handler(cfg),
		// A slow or hung client must not hold a connection open indefinitely.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	log.Printf("workout-mcp %s listening on %s (mcp at %s/mcp)", version, addr, cfg.PublicURL)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("workout-mcp: %v", err)
	}
}
