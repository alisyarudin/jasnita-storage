VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all web build build-linux test run clean

all: web build

web: ## build console Vue -> internal/console/dist
	cd web && npm install --no-audit --no-fund && npm run build

build: ## build binary untuk mesin ini
	go build -ldflags '$(LDFLAGS)' -o bin/jasstore ./cmd/jasstore

build-linux: ## build binary linux/amd64 (deploy ke server)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o bin/jasstore-linux-amd64 ./cmd/jasstore

test:
	go test ./...

run: build
	./bin/jasstore

clean:
	rm -rf bin internal/console/dist/assets
