# restockd 构建：先构建前端再内嵌进 Go 二进制
GOOS ?= linux
GOARCH ?= amd64
BIN := bin/restockd-$(GOOS)-$(GOARCH)

.PHONY: web build gobuild test run

web:
	cd web && npm ci && npm run build

gobuild:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "-s -w" -o $(BIN) ./cmd/restockd

build: web gobuild

test:
	go test -race ./...
	cd web && npm run test

run:
	RESTOCKD_PASSWORD=$${RESTOCKD_PASSWORD:-dev} go run ./cmd/restockd -debug
