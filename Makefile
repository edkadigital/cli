BINARY := edka
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)
# CI runs the same versions; see .github/workflows/ci.yml.
GOLANGCI_LINT_VERSION := v2.14.0
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.8.0

.PHONY: build install test check lint vuln skill app-skill clean
build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/edka
install:
	go install -trimpath -ldflags '$(LDFLAGS)' ./cmd/edka
test:
	go test -race ./...
check: lint
	test -z "$$(gofmt -l cmd internal skills)"
	go vet ./...
	go build ./...
lint:
	@command -v golangci-lint >/dev/null || { echo "golangci-lint is not installed. Install $(GOLANGCI_LINT_VERSION): https://golangci-lint.run/docs/welcome/install/"; exit 1; }
	golangci-lint run ./...
# skill rewrites the command reference of the agent skill in skills/edka.
skill:
	go test ./internal/cli -run '^TestAgentSkillMatchesCommands$$' -count=1 -update-skill
# app-skill copies the edka-app skill from the main branch of
# github.com/edkadigital/apps, which keeps the package format the skill teaches.
# A release checks that its copy is current.
app-skill:
	@tmp=$$(mktemp -d) && trap 'rm -rf "$$tmp"' EXIT && \
	curl -fsSL https://codeload.github.com/edkadigital/apps/tar.gz/refs/heads/main | tar -xz -C "$$tmp" && \
	rm -rf skills/edka-app && cp -R "$$tmp/apps-main/skills/edka-app" skills/edka-app
# vuln reads the Go vulnerability database, so it needs the network.
vuln:
	go run $(GOVULNCHECK) ./...
clean:
	rm -rf bin
