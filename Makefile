.PHONY: test build run fmt

test:
	go test ./...

build:
	go build -trimpath -o location-bot .

run:
	go run .

fmt:
	gofmt -w main.go internal
