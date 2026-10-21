.PHONY: dev build test vet check fmt

dev:
	direnv exec . go tool mygo dev

build:
	direnv exec . go tool mygo build -platform darwin/arm64 -skip-dmg

test:
	direnv exec . go test ./...

vet:
	direnv exec . go vet ./...
	direnv exec . go tool mygo vet .

check: test vet

fmt:
	direnv exec . go fmt ./...
