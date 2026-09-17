// Command workout-mcp exposes a restricted workout-prescription surface to MCP
// clients such as ChatGPT.
//
// Deliberately almost empty: everything that decides what may happen lives
// behind the API this talks to, and a server that made decisions of its own
// would be a second place to get them wrong.
package main

import (
	"context"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hoseacodes/manifestfitness/workout-mcp/internal/client"
	"github.com/hoseacodes/manifestfitness/workout-mcp/internal/tools"
)

func main() {
	// The token is obtained through the athlete's consent flow. This server
	// never mints one, and holds no signing key with which it could.
	api, err := client.New(
		os.Getenv("MANIFEST_API_BASE_URL"),
		os.Getenv("MANIFEST_SERVICE_TOKEN"),
	)
	if err != nil {
		// Refuse to start rather than appear healthy and fail on the athlete's
		// first request.
		log.Fatalf("workout-mcp: %v", err)
	}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "workout-mcp",
		Version: "0.1.0",
	}, nil)

	tools.Register(server, api)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("workout-mcp: %v", err)
	}
}
