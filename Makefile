VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
DEFAULT_UMAMI_HOST := https://a.kunchenguid.com
DEFAULT_UMAMI_WEBSITE_ID := f959e889-92f5-4121-8a1f-571b10861198
DOTENV_FILE ?= .env
DOTENV_NS_UMAMI_HOST := $(shell [ -f "$(DOTENV_FILE)" ] && perl -ne 'next if /^\s*(?:\#|$$)/; s/^\s*export\s+//; next unless /^\s*NS_UMAMI_HOST\s*=\s*(.*)$$/; $$v=$$1; $$v =~ s/^\s+|\s+$$//g; if ($$v =~ /^( ["\x27] )(.*)\1$$/x) { $$v=$$2; } else { $$v =~ s/\s+\#.*$$//; $$v =~ s/\s+$$//; } $$out=$$v; END { print $$out if defined $$out }' "$(DOTENV_FILE)")
DOTENV_LEGACY_UMAMI_HOST := $(shell [ -f "$(DOTENV_FILE)" ] && perl -ne 'next if /^\s*(?:\#|$$)/; s/^\s*export\s+//; next unless /^\s*NO_MISTAKES_UMAMI_HOST\s*=\s*(.*)$$/; $$v=$$1; $$v =~ s/^\s+|\s+$$//g; if ($$v =~ /^( ["\x27] )(.*)\1$$/x) { $$v=$$2; } else { $$v =~ s/\s+\#.*$$//; $$v =~ s/\s+$$//; } $$out=$$v; END { print $$out if defined $$out }' "$(DOTENV_FILE)")
DOTENV_NS_UMAMI_WEBSITE_ID := $(shell [ -f "$(DOTENV_FILE)" ] && perl -ne 'next if /^\s*(?:\#|$$)/; s/^\s*export\s+//; next unless /^\s*NS_UMAMI_WEBSITE_ID\s*=\s*(.*)$$/; $$v=$$1; $$v =~ s/^\s+|\s+$$//g; if ($$v =~ /^(["\x27])(.*)\1$$/) { $$v=$$2; } else { $$v =~ s/\s+\#.*$$//; $$v =~ s/\s+$$//; } $$out=$$v; END { print $$out if defined $$out }' "$(DOTENV_FILE)")
DOTENV_LEGACY_UMAMI_WEBSITE_ID := $(shell [ -f "$(DOTENV_FILE)" ] && perl -ne 'next if /^\s*(?:\#|$$)/; s/^\s*export\s+//; next unless /^\s*NO_MISTAKES_UMAMI_WEBSITE_ID\s*=\s*(.*)$$/; $$v=$$1; $$v =~ s/^\s+|\s+$$//g; if ($$v =~ /^(["\x27])(.*)\1$$/) { $$v=$$2; } else { $$v =~ s/\s+\#.*$$//; $$v =~ s/\s+$$//; } $$out=$$v; END { print $$out if defined $$out }' "$(DOTENV_FILE)")
ifneq ($(strip $(DOTENV_NS_UMAMI_HOST)),)
ifneq ($(strip $(DOTENV_LEGACY_UMAMI_HOST)),)
ifneq ($(DOTENV_NS_UMAMI_HOST),$(DOTENV_LEGACY_UMAMI_HOST))
$(error NS_UMAMI_HOST and NO_MISTAKES_UMAMI_HOST configure the same setting with different values)
endif
endif
endif
ifneq ($(strip $(DOTENV_NS_UMAMI_WEBSITE_ID)),)
ifneq ($(strip $(DOTENV_LEGACY_UMAMI_WEBSITE_ID)),)
ifneq ($(DOTENV_NS_UMAMI_WEBSITE_ID),$(DOTENV_LEGACY_UMAMI_WEBSITE_ID))
$(error NS_UMAMI_WEBSITE_ID and NO_MISTAKES_UMAMI_WEBSITE_ID configure the same setting with different values)
endif
endif
endif
DOTENV_UMAMI_HOST := $(if $(DOTENV_NS_UMAMI_HOST),$(DOTENV_NS_UMAMI_HOST),$(DOTENV_LEGACY_UMAMI_HOST))
DOTENV_UMAMI_WEBSITE_ID := $(if $(DOTENV_NS_UMAMI_WEBSITE_ID),$(DOTENV_NS_UMAMI_WEBSITE_ID),$(DOTENV_LEGACY_UMAMI_WEBSITE_ID))
override UMAMI_HOST := $(if $(DOTENV_UMAMI_HOST),$(DOTENV_UMAMI_HOST),$(if $(UMAMI_HOST),$(UMAMI_HOST),$(DEFAULT_UMAMI_HOST)))
override UMAMI_WEBSITE_ID := $(if $(DOTENV_UMAMI_WEBSITE_ID),$(DOTENV_UMAMI_WEBSITE_ID),$(if $(UMAMI_WEBSITE_ID),$(UMAMI_WEBSITE_ID),$(DEFAULT_UMAMI_WEBSITE_ID)))
LDFLAGS := -X 'github.com/Blakeolson21/no-slop/internal/buildinfo.Version=$(VERSION)' \
           -X 'github.com/Blakeolson21/no-slop/internal/buildinfo.Commit=$(COMMIT)' \
           -X 'github.com/Blakeolson21/no-slop/internal/buildinfo.Date=$(DATE)' \
           -X 'github.com/Blakeolson21/no-slop/internal/buildinfo.TelemetryHost=$(UMAMI_HOST)' \
           -X 'github.com/Blakeolson21/no-slop/internal/buildinfo.TelemetryWebsiteID=$(UMAMI_WEBSITE_ID)'

.PHONY: build dist install test e2e e2e-record lint fmt clean docs docs-build docs-preview demo skill skill-check

DIST_DIR ?= dist
BIN_DIR ?= bin
NO_SLOP_CMD ?= ./cmd/no-slop
NO_MISTAKES_CMD ?= ./cmd/no-mistakes
NOSLOP_CMD ?= ./cmd/noslop
INSTALL_BIN := $(shell go env GOPATH)/bin/no-slop

build:
	mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/no-slop $(NO_SLOP_CMD)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/no-mistakes $(NO_MISTAKES_CMD)
	go build -o $(BIN_DIR)/noslop $(NOSLOP_CMD)

dist:
	rm -rf $(DIST_DIR)
	mkdir -p $(DIST_DIR)
	for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64; do \
		os=$${target%/*}; \
		arch=$${target#*/}; \
		bin=no-slop; \
		legacy=no-mistakes; \
		out="$(DIST_DIR)/$$bin"; \
		if [ "$$os" = "windows" ]; then \
			bin="$$bin.exe"; \
			legacy="$$legacy.exe"; \
			out="$(DIST_DIR)/$$bin"; \
		fi; \
		CGO_ENABLED=0 GOOS="$$os" GOARCH="$$arch" go build -ldflags "$(LDFLAGS)" -o "$$out" $(NO_SLOP_CMD); \
		cp "$$out" "$(DIST_DIR)/$$legacy"; \
		if [ "$$os" = "windows" ]; then \
			( cd "$(DIST_DIR)" && zip -q "no-slop-$(VERSION)-$$os-$$arch.zip" "$$bin" ); \
			( cd "$(DIST_DIR)" && zip -q "no-mistakes-$(VERSION)-$$os-$$arch.zip" "$$legacy" ); \
		else \
			tar -C "$(DIST_DIR)" -czf "$(DIST_DIR)/no-slop-$(VERSION)-$$os-$$arch.tar.gz" "$$bin"; \
			tar -C "$(DIST_DIR)" -czf "$(DIST_DIR)/no-mistakes-$(VERSION)-$$os-$$arch.tar.gz" "$$legacy"; \
		fi; \
		rm -f "$$out" "$(DIST_DIR)/$$legacy"; \
	done

install: build
	mkdir -p $(dir $(INSTALL_BIN))
	install -m 755 $(BIN_DIR)/no-slop $(INSTALL_BIN)
	install -m 755 $(BIN_DIR)/no-mistakes $(dir $(INSTALL_BIN))/no-mistakes
	$(INSTALL_BIN) daemon stop
	$(INSTALL_BIN) daemon start

test:
	go test -race ./...

# End-to-end suite: drives the real no-slop binary against a fake
# agent through the full push -> pipeline -> push journey for each
# e2e-covered agent backend, plus the step-local e2e tests that live
# next to the pipeline-step code (e.g. coverage provider journeys).
# Excluded from `make test` because it is behind the `e2e` build tag and
# rebuilds binaries on each run.
#
# scripts/e2e.sh owns temporary-daemon inventory + EXIT/INT/TERM reaping so
# an interrupted or timed-out go test child cannot leave detached e2e
# daemons behind. Keepalive shells are out of scope. A SIGKILL of the
# wrapper shell itself does not run its trap; next-run pre-reap recovers.
e2e:
	@bash scripts/e2e.sh

# Re-record fixtures from the real claude/codex/opencode CLIs and overwrite
# internal/e2e/fixtures/. Spends real API quota — run only when the upstream
# wire format changes or when adding a new flavour. Personal paths are
# scrubbed automatically; review the diff before committing.
e2e-record:
	go run ./cmd/recordfixture claude   --out internal/e2e/fixtures/claude
	go run ./cmd/recordfixture codex    --out internal/e2e/fixtures/codex
	go run ./cmd/recordfixture opencode --out internal/e2e/fixtures/opencode

# Regenerate the committed agent skill (skills/no-slop/SKILL.md) from the
# internal/skill source of truth.
skill:
	go run ./cmd/genskill

# Fail if the committed skill has drifted from the generator. Wired into lint
# so CI catches a forgotten `make skill`.
skill-check:
	go run ./cmd/genskill --check

lint: skill-check
	go vet ./...

fmt:
	gofmt -w .

docs: docs-build

docs-build:
	cd docs && npm ci && npm run build

docs-preview:
	cd docs && npm run preview

demo: build
	vhs demo.tape
	ffmpeg -i demo_raw.gif -filter_complex "\
		[0:v]split[orig][zoom_src];\
		[zoom_src]crop=963:570:0:0,scale=1100:650:flags=lanczos[zoomed];\
		[orig]scale=1100:650:flags=lanczos[base];\
		[base][zoomed]overlay=0:0:enable='lt(t,4.04)',setpts=1.9*PTS,\
		split[s0][s1];\
		[s0]palettegen=max_colors=128[p];\
		[s1][p]paletteuse=dither=sierra2_4a\
	" -r 10 -y demo.gif
	ffmpeg -i demo_raw.gif -filter_complex "\
		[0:v]split[orig][zoom_src];\
		[zoom_src]crop=963:570:0:0,scale=1100:650:flags=lanczos[zoomed];\
		[orig]scale=1100:650:flags=lanczos[base];\
		[base][zoomed]overlay=0:0:enable='lt(t,4.04)',setpts=1.9*PTS\
	" -c:v libx264 -pix_fmt yuv420p -movflags +faststart -r 30 -y demo.mp4
	rm -f demo_raw.gif

clean:
	rm -rf bin/
