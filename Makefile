VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build run test cover vet lint e2e docker snapshot demo clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/kropka ./cmd/kropka

run: build
	./bin/kropka

test:
	go test -race ./...

cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -n 1

vet:
	go vet ./...

lint:
	golangci-lint run
	biome lint

# Browser tests of the UI against a real kropka (see e2e/). The first run may
# need `cd e2e && npx playwright install chromium`.
e2e: e2e/node_modules
	cd e2e && npx playwright test

e2e/node_modules: e2e/package-lock.json
	cd e2e && npm ci
	touch $@

docker:
	docker build --build-arg VERSION=$(VERSION) -t kropka:$(VERSION) .

# Build all release artifacts locally without publishing.
snapshot:
	goreleaser release --snapshot --clean

# Record the README demo into docs/demo/out (see docs/demo/README.md).
demo:
	docs/demo/record.sh

clean:
	rm -rf bin dist
