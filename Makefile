.PHONY: clean test lint test app

all: lint app

clean:
	@rm -rf build

lint:
	@gofmt -w ./app

test:
	@go test -v ./test

app:
	@mkdir -p build
	@go build -ldflags "-s -w" -o build/cronotopia ./app

