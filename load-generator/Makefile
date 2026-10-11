IMAGE ?= loadgen
VERSION ?= 1.0.0

.PHONY: build test vet check docker clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-X main.version=$(VERSION)" -o bin/loadgen ./cmd/loadgen

test:
	go test -race ./...

vet:
	go vet ./...

check: vet test

docker:
	docker build --platform linux/amd64 --build-arg VERSION=$(VERSION) -t $(IMAGE) .

clean:
	rm -rf bin
