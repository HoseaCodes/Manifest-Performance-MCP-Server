package httpserver

import (
	"context"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

/*
A deliberately minimal MCP server, for isolating a client that accepts the
handshake and then exposes no tools.

ChatGPT completes OAuth, completes `initialize`, calls `tools/list`, receives
four valid tools — and reports `MCP servers: none`. Two other MCP clients use
the same endpoint without trouble, so the question is whether the connector
platform is failing to bind *anything*, or choking on something in these
particular schemas.

One tool answers that. `ping` takes no arguments and returns a string: no
unions, no `$ref`, no formats, no defaults, no optional fields, nothing nested.
If it appears in ChatGPT, the problem is specific to the real schemas and can be
bisected by adding them back one at a time. If it does not, nothing about this
server's tools is responsible.

Mounted beside the real endpoint rather than replacing it, so the working
surface keeps working while the experiment runs.
*/

type pingInput struct{}

func minimalServer(version string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "workout-mcp-minimal",
		Version: version,
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "ping",
		Description: "Returns the string pong. Takes no arguments. Used to verify that this connector can expose a tool at all.",
		// Written out rather than inferred: inference is what produced the
		// nullable unions that a strict client rejected, and the point of this
		// tool is to contain nothing that could be rejected.
		InputSchema: &jsonschema.Schema{
			Type:                 "object",
			Properties:           map[string]*jsonschema.Schema{},
			AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
		},
	}, func(context.Context, *mcp.CallToolRequest, pingInput) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: "pong"}},
		}, nil, nil
	})

	return server
}
