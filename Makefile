# claudezilla-go
#   make            build bin/claudezilla-host and bin/claudezilla-mcp
#   make test       Go tests, then the extension's and the black-box MCP tests (node --test)
#   make xpi        reproducible unsigned XPI in dist/
#   make sign       have Mozilla sign it (unlisted); needs AMO_JWT_ISSUER and AMO_JWT_SECRET
#   make dist       static binaries for Linux, macOS and Windows in dist/<os>-<arch>/

GOFLAGS_BUILD := -trimpath -ldflags "-s -w"
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: build test xpi sign dist clean
build:
	CGO_ENABLED=0 go build $(GOFLAGS_BUILD) -o bin/ ./cmd/...

test: build
	go vet ./...
	go test ./...
	node --test

xpi:
	python3 -I scripts/build-xpi.py

sign: xpi
	python3 -I scripts/sign-xpi.py dist/$$(sed -n 's/.*"id": *"\([^"]*\)".*/\1/p' extension/manifest.json)-$$(sed -n 's/.*"version": *"\([^"]*\)".*/\1/p' extension/manifest.json | head -1).unsigned.xpi

dist:
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; \
	  echo "$$os/$$arch"; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS_BUILD) -o dist/$$os-$$arch/ ./cmd/... || exit 1; \
	done

clean:
	go clean
	rm -rf ./bin ./dist
