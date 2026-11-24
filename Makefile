GO ?= go
BIN := bin
GORELEASER := $(GO) run github.com/goreleaser/goreleaser/v2@v2.20.2

.PHONY: build test release-check snapshot

# Entwicklungsstände tragen die Commit-Kurzform (mit -dirty bei offenen
# Änderungen), nie eine Version aus einem Tag: Die vergibt nur GoReleaser
# beim Release (siehe README „Releases“). --exclude='*' lässt jeden Tag
# außer Acht, --always nimmt dann den Commit.
VERSION ?= $(shell git describe --always --dirty --exclude='*' 2>/dev/null || echo dev)

# Koordinator für das eigene System, Starter immer für Windows – als
# GUI-Programm (-H=windowsgui: kein Konsolenfenster beim Tray; die
# Unterbefehle hängen sich an die Konsole des Aufrufers).
build:
	$(GO) build -o $(BIN)/wanderwelt-koordinator ./cmd/wanderwelt-koordinator
	GOOS=windows $(GO) build -ldflags "-H=windowsgui -X main.version=$(VERSION)" -o $(BIN)/wanderwelt.exe ./cmd/wanderwelt

test:
	$(GO) test ./...

# .goreleaser.yaml prüfen.
release-check:
	$(GORELEASER) check

# Alle Release-Archive nach dist/ bauen, ohne etwas hochzuladen.
snapshot:
	$(GORELEASER) release --snapshot --clean
