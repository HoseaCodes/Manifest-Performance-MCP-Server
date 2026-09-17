# Manifest Performance MCP Server

An MCP server, in Go, exposing four tools over the `manifestfitness` internal
API.

```
ChatGPT ──MCP/stdio──▶ workout-mcp ──HTTPS──▶ /api/internal/me/* ──▶ MongoDB
                        (this binary)          (manifestfitness)
```

## Why a separate Go module

Phase 5's exit criterion is a proof of absence: **no database access**. Go makes
that a stronger claim than a bundled JavaScript package could. The module graph
is explicit and complete, so `go list -deps` answers "can anything in this
binary reach MongoDB" definitively, rather than leaving it a question about what
a bundler happened to include.

`go test ./...` asserts it, and the assertion has been verified to fail when a
violation is introduced.

## Tools

| Tool | Scope required |
|---|---|
| `get_generation_context` | `training:read` |
| `create_workout_prescription` | `workouts:write` |
| `get_workout_prescription` | `workouts:read` |
| `supersede_workout_prescription` | `workouts:write` |

## What it cannot do, structurally

- **Choose an athlete.** Every call is `/me`; the athlete comes from the token's
  `sub`. No input struct has a field for one — a test enforces that.
- **Mark a workout completed.** `CreateInput` has no `Status` field, and no such
  endpoint exists on the internal surface.
- **Claim a session came from a coach.** `source` is stamped `ai_generated`
  server-side and is not an input.
- **Record readiness or execution data.** Athlete-authenticated routes only.
- **Delete anything.** No delete endpoint exists for any caller.
- **Reach a database.** No driver in the module graph, no connection string in
  source.

These are properties of the surface it can reach, not promises this code makes.
A restriction enforced by absence cannot be relaxed by editing this package.

## Configuration

| Variable | Meaning |
|---|---|
| `MANIFEST_API_BASE_URL` | Base URL of the `manifestfitness` API |
| `MANIFEST_SERVICE_TOKEN` | Delegated token: `sub` = athlete, `act.sub` = this service |

The token comes from the athlete's consent flow. This server never mints one and
holds no signing key with which it could. A missing variable is a startup
failure, not a first-request failure.

## Running

```sh
go build -o bin/workout-mcp .
MANIFEST_API_BASE_URL=https://www.manifestathletics.com \
MANIFEST_SERVICE_TOKEN=... \
  ./bin/workout-mcp
```

Speaks MCP over stdio.
