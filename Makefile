.PHONY: dev build release runtime-bundle test vet check fmt assets icon
.DEFAULT_GOAL := dev

MINGW_CC ?= x86_64-w64-mingw32-gcc
BOOTSTRAP := internal/launch/assets/endfield-bootstrap.exe
RUNTIME_SOURCE ?= $(HOME)/Library/Application Support/Gryph Launcher/Runtime/current
RUNTIME_OUTPUT ?= resources/runtime
RUNTIME_URL ?=

assets: $(BOOTSTRAP)

$(BOOTSTRAP): tools/endfield-bootstrap/main.c Makefile
	mkdir -p $(@D)
	direnv exec . $(MINGW_CC) -std=c11 -O2 -Wall -Wextra -Werror -municode -fno-ident -frandom-seed=gryph-endfield-bootstrap-v1 -Wl,--no-insert-timestamp $< -o $@.tmp -lshlwapi
	mv $@.tmp $@

dev: assets
	direnv exec . go tool mygo dev

icon:
	sips -s format png assets/icon.svg --out resources/icon.png

runtime-bundle:
	direnv exec . go run ./internal/wine/packagecmd -source "$(RUNTIME_SOURCE)" -output "$(RUNTIME_OUTPUT)" $(if $(RUNTIME_URL),-url "$(RUNTIME_URL)")

build: assets icon runtime-bundle
	direnv exec . go tool mygo build -platform darwin/arm64 -skip-dmg

release: assets icon runtime-bundle
	direnv exec . go tool mygo build -platform darwin/arm64

test: assets
	direnv exec . go test ./...

vet: assets
	direnv exec . go vet ./...
	direnv exec . go tool mygo vet .

check: test vet

fmt:
	direnv exec . go fmt ./...
