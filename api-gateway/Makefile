APP     := api-gateway
MODULE  := github.com/huzaifa678/SAAS-services
BINARY  := bin/$(APP)

.PHONY: build run test test-unit test-integration lint swag clean

build:
	go build -o $(BINARY) ./...

server:
	go run main.go

test: test-unit test-integration

test-unit:
	go test ./circuit/... ./interceptor/... ./transport/... ./service/... -v

test-integration:
	go test ./integration/... -v

lint:
	go vet ./...

swag:
	swag init

clean:
	rm -rf bin/
