# Makefile — local development for the eventplane library. It is a library, not
# a service: no build/run target, just the standard quality gates.
.PHONY: test vet fmt

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .
