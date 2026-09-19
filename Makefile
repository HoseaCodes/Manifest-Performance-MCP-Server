# Build and deploy targets.
#
# Deployed to Fly.io rather than Lambda. Two reasons, both found by testing
# rather than by reading: Lambda Function URLs rewrite `WWW-Authenticate` to
# `x-amzn-Remapped-www-authenticate`, so an MCP client never sees the challenge
# that tells it where to authenticate; and Lambda invocations do not share
# memory, which forced MCP sessions into stateless mode.

BINARY := workout-mcp
APP    ?= workout-mcp

.PHONY: build test docker deploy logs clean

build:
	go build -o bin/$(BINARY) .

test:
	gofmt -l . | (! grep .) && go vet ./... && go test ./...

docker:
	docker build -t $(BINARY):local .

deploy:
	flyctl deploy --app $(APP) --ha=false

logs:
	flyctl logs --app $(APP)

clean:
	rm -rf bin
