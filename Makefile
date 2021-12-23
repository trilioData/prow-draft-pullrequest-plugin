GO_APP_BINARY ?= main
IMAGE ?= draft-plugin

clean:
	go clean
	rm -f $(GO_APP_BINARY)

build:
	CGO_ENABLED=0 go build -o $(GO_APP_BINARY) ./cmd/main.go

fmt:
	go fmt ./...

vet:
	go vet ./...


.PHONY: clean build
