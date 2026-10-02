VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build run test vet docker snapshot clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/kropka ./cmd/kropka

run: build
	./bin/kropka

test:
	go test -race ./...

vet:
	go vet ./...

docker:
	docker build --build-arg VERSION=$(VERSION) -t kropka:$(VERSION) .

# Build all release artifacts locally without publishing.
snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf bin dist
