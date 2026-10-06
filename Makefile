BINARY := cracklet
GOFLAGS ?=

ENVD := internal/envdbin/cracklet-envd

.PHONY: build install test cover lint e2e clean envd

# The guest identity daemon is cross-compiled for the microVMs and embedded into cracklet.
envd: $(ENVD)

$(ENVD): $(wildcard cmd/cracklet-envd/*.go) $(wildcard internal/envd/*.go) go.mod
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o $(ENVD) ./cmd/cracklet-envd

build: $(ENVD)
	go build $(GOFLAGS) -o bin/$(BINARY) ./cmd/$(BINARY)

install: $(ENVD)
	go install $(GOFLAGS) ./cmd/$(BINARY)

test: $(ENVD)
	go test -race ./...

cover: $(ENVD)
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

lint:
	gofmt -l . | tee /dev/stderr | test -z "$$(cat)"
	go vet ./...
	shellcheck -s bash internal/agent/agent.sh

# Boots a real microVM; needs a prepared host (cracklet prepare). Always cleans up.
e2e: build
	./bin/$(BINARY) new e2e-smoke --mem 512 \
	  && ./bin/$(BINARY) ssh e2e-smoke uname -a \
	  && ./bin/$(BINARY) ls; status=$$?; \
	./bin/$(BINARY) rm e2e-smoke; exit $$status

clean:
	rm -rf bin coverage.out $(ENVD)
