# Two stages: build with the toolchain, ship without it.
#
# The result is a static binary on a distroless base — no shell, no package
# manager, nothing to exec into. For a service whose entire job is holding other
# people's credentials in flight, the smallest possible surface is worth the
# mild inconvenience of not being able to poke at a running container.

FROM golang:1.27-alpine AS build
WORKDIR /src

# Dependencies first, so a source-only change does not re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO off so the binary is static and runs on a base with no libc.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/workout-mcp .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/workout-mcp /workout-mcp

# HTTP mode, not stdio. Fly runs one long-lived process, so unlike Lambda this
# can keep MCP sessions in memory — `Stateless` stays off.
ENV MCP_TRANSPORT=http
EXPOSE 8080

USER nonroot:nonroot
ENTRYPOINT ["/workout-mcp"]
