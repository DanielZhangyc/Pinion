.PHONY: dev build release runtime-bundle test vet check fmt assets icon
.DEFAULT_GOAL := dev

MINGW_CC ?= x86_64-w64-mingw32-gcc
BOOTSTRAP := internal/launch/assets/endfield-bootstrap.exe
HPATCH := internal/download/assets/pinion-hpatch
HPATCH_SOURCE := third_party/hdiffpatch/HPatch
RUNTIME_SOURCE ?= $(HOME)/Library/Application Support/Gryph Launcher/Runtime/current
RUNTIME_OUTPUT ?= resources/runtime
RUNTIME_URL ?=

assets: $(BOOTSTRAP) $(HPATCH)

$(HPATCH): tools/hpatch/main.c $(wildcard $(HPATCH_SOURCE)/*.h) $(HPATCH_SOURCE)/hpatch_mt/hpatch_mt.h $(HPATCH_SOURCE)/patch.c Makefile
	mkdir -p $(@D)
	xcrun clang -arch arm64 -mmacosx-version-min=15.0 -std=c11 -O2 -Wall -Wextra -Wno-unused-parameter -D_IS_USED_MULTITHREAD=0 -D_HPATCH_IS_USED_MULTITHREAD=0 -I$(HPATCH_SOURCE) tools/hpatch/main.c $(HPATCH_SOURCE)/patch.c -o $@.tmp
	codesign --force --sign - $@.tmp
	mv $@.tmp $@

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
