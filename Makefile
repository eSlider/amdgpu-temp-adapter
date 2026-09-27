BIN := amdgpu-temp-adapter

.PHONY: all build test vet clean

all: build

build:
	go build -trimpath -o $(BIN) .

test:
	go test -race ./...

vet:
	go vet ./...

clean:
	rm -f $(BIN)
