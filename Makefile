BINARY := edka
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)
# CI runs the same versions; see .github/workflows/ci.yml.
GOLANGCI_LINT_VERSION := v2.14.0
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.8.0

.PHONY: build install test check lint vuln clean
build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/edka
install:
	go install -trimpath -ldflags '$(LDFLAGS)' ./cmd/edka
test:
	go test -race ./...
check: lint
	test -z "$$(gofmt -l cmd internal)"
	go vet ./...
	go build ./...
lint:
	@command -v golangci-lint >/dev/null || { echo "golangci-lint is not installed. Install $(GOLANGCI_LINT_VERSION): https://golangci-lint.run/docs/welcome/install/"; exit 1; }
	golangci-lint run ./...
# vuln reads the Go vulnerability database, so it needs the network.
vuln:
	go run $(GOVULNCHECK) ./...
clean:
	rm -rf bin
