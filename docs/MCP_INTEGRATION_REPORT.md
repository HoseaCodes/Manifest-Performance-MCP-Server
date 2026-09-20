# Workout Coach MCP server — integration report

For OpenAI support, or anyone picking up the ChatGPT connector problem.

**Summary: the MCP server is conformant and working. ChatGPT connects to it,
authenticates, retrieves the tools, and then reports that the connector exposes
none.** Two other MCP clients use the same endpoint without incident.

---

## The server

| | |
|---|---|
| Endpoint | `https://workout-mcp.fly.dev/mcp` |
| Transport | Streamable HTTP, stateful, SSE on `GET` |
| Protocol | `2025-06-18` (also serves `2025-03-26`, `2024-11-05`) |
| Auth | OAuth 2.1 + PKCE, RFC 7591 dynamic client registration |
| Implementation | Go, `modelcontextprotocol/go-sdk` v1.8.0 |

## `tools/list` returns four tools

```
create_workout_prescription      desc=434ch  props=12  required=[localDate, sessionOfDay, scheduledTimezone, name, sections]
get_generation_context           desc=557ch  props=5   required=[]
get_workout_prescription         desc=109ch  props=1   required=[prescriptionId]
supersede_workout_prescription   desc=341ch  props=14  required=[slotId, expectedRevision, localDate, sessionOfDay, ...]
```

Each has a unique name, a description, a valid JSON Schema and a registered
handler. Verified with the official MCP Inspector at `--strict`: **0 errors,
0 warnings.**

> ChatGPT's own diagnosis asserted the server should expose `list_options`,
> `get_generation_context` and `create_workout_prescription`. **`list_options`
> has never existed**, and the list omits two tools that do. That is a
> confabulation produced from an empty tool list, not a reading of this server,
> and it should not be treated as evidence about the server's contents.

## Clients that work

| Client | Result |
|---|---|
| MCP Inspector (official, `@modelcontextprotocol/inspector`) | Lists all four tools; **calls them successfully against production** |
| Claude Code (`claude mcp add --transport http`) | `✓ Connected` on the first attempt |
| ChatGPT | Connects, authenticates, calls `tools/list` — then reports no callable actions |

## What ChatGPT actually did, from the server's own logs

```
07:16:18  initialize
07:16:18  notifications/initialized
07:16:18  tools/list            ← succeeded, returned four tools
```

That sequence occurred at 07:16, 07:19 and 07:31. On every occasion ChatGPT
reported "no callable tools". **The tools reached it and were not bound to the
connector.**

Its own registration diagnostic:

```
Plugin: Workout Coach v1.0.0
Skills: none
MCP servers: none
Callable connector actions: none
```

`v1.0.0` is ChatGPT's app metadata; this server identifies itself as `0.2.0`.
`MCP servers: none` on a connector created from an MCP URL, whose `tools/list`
demonstrably succeeded, is the defect.

## Checks already performed, and passing

- Server starts without error; health check passing continuously
- Endpoint publicly reachable over HTTPS, not bound to localhost
- Handshake completes: `initialize` → `notifications/initialized` → `tools/list`
- CORS preflight (`OPTIONS`) returns 204 without a credential
- `GET /mcp` returns `200 text/event-stream`; `DELETE` returns 204
- Unauthenticated requests return `401` with `WWW-Authenticate` pointing at
  `/.well-known/oauth-protected-resource` — the documented way to trigger OAuth,
  and it does not block discovery for an authenticated client
- OAuth completes end to end: authorization code issued, redeemed, access and
  refresh tokens granted, all three scopes present on the grant
- Dynamic client registration works; ChatGPT self-registers successfully
- Latest code deployed; request logging confirms live traffic arriving

## Defects found and fixed here (all on the server, all resolved)

These were real, and any strict MCP client would have hit most of them:

1. **CORS preflight returned 401.** `OPTIONS` carries no `Authorization` header,
   so the bearer check rejected it and the real request was never sent.
2. **Stateless mode returned 405 for `GET` and `DELETE`**, which a streamable
   HTTP client needs for its event stream and session teardown.
3. **Nullable type unions in tool schemas.** Go pointer fields inferred as
   `"type": ["null","integer"]`; strict clients reject such tools. Rewritten as
   `anyOf` branches — 47 instances, now 0.
4. **Authorization server did not support HTTP Basic client authentication**
   (RFC 6749 §2.3.1), so token exchange failed for clients that default to it.
5. **Authorization codes expired after 60 seconds**, shorter than a
   browser-to-backend exchange takes.
6. **No RFC 8414 metadata and no RFC 7591 registration endpoint.** Both added.

## Two further findings

**A single tool with an empty schema fares no better.** A second endpoint,
`/mcp-min`, exposes one tool — `ping`, no arguments,
`{"type":"object","properties":{},"additionalProperties":false}`, no unions, no
`$ref`, nothing nested. A connector pointed at it binds zero actions in exactly
the same way. **Nothing about the real tools' schemas is responsible.**

**Answering SEP-2575 `server/discover` makes it worse.** When the SDK answers
`-32601`, ChatGPT falls back to `initialize` + `tools/list` and receives the
tools. When the probe is answered — honestly, advertising the versions this
server actually speaks — ChatGPT stops at that point and never requests tools at
all. The refusal is therefore the better behaviour with this client, and the
shim has been removed.

**The client's behaviour is not consistent between attempts.** On the same
connector and server, within twenty minutes:

```
20:54:45  /mcp  initialize → notifications/initialized → tools/list   (tools delivered)
21:11:33  /mcp  POST with an empty body
21:14:10  /mcp  POST with an empty body  (x3)
21:14:29  /mcp  server/discover, then nothing
```

These requests contain no parseable JSON-RPC body and cannot be correlated with
a standard MCP operation implemented by this server. Whether they are an
undocumented platform probe is not something this end can determine.

## Conclusion

ChatGPT's connector platform successfully authenticates and, in some attempts,
completes `initialize` and `tools/list`, receiving valid tools. It nevertheless
binds zero callable actions and reports `MCP servers: none`. The failure
reproduces against a separate endpoint exposing only a schema-free `ping` tool,
ruling out the production tools and their JSON Schemas. Other MCP clients work
against the same server and protocol.

This is an OpenAI-side connector defect or an undocumented compatibility
requirement, not an ordinary MCP server implementation failure.

## Requested from OpenAI

1. Whether SEP-2575 `server/discover` is currently **required, optional,
   experimental or unsupported** for ChatGPT connectors. Answering the probe and
   refusing it produce different failures here, and neither is documented.
2. Why the platform reports `MCP servers: none` after successfully contacting an
   MCP endpoint and receiving a valid tool list.
3. Why ChatGPT sometimes sends POST requests with no parseable JSON-RPC body.
4. The internal rejection reason when a returned tool list produces zero
   registered actions — that reason is not surfaced anywhere in the UI or to the
   server.
5. Clearing or rebuilding any cached connector metadata for this plugin id.
6. Whether creating a new development plugin id is the only available way to
   bypass that cache.

**Plugin id:** `dev-6aae57cdfb40819198ef42dde71fda5d`
**Endpoints:** `https://workout-mcp.fly.dev/mcp` (four tools),
`https://workout-mcp.fly.dev/mcp-min` (one schema-free `ping` tool)
