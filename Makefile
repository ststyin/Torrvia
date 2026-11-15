VERSION ?= 1.0.0
GO      ?= go
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: help build run test fmt vet docker docker-multi clean token check

help:
	@echo "build        Build the native binary"
	@echo "run          Build and run"
	@echo "test         Run tests"
	@echo "fmt / vet    Format / run static checks"
	@echo "check        Check sites and downloader after editing config.toml"
	@echo "docker       Build a native container image"
	@echo "docker-multi Build amd64 + arm64 container images"
	@echo "token        Print the current access token"
	@echo "clean        Remove build output"

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o torrvia .

run: build
	./torrvia

test:
	$(GO) test ./...

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

docker:
	docker build --build-arg VERSION=$(VERSION) -t torrvia:$(VERSION) .

docker-multi:
	docker buildx build --platform linux/amd64,linux/arm64 \
		--build-arg VERSION=$(VERSION) -t torrvia:$(VERSION) .

token:
	@./torrvia -token

check: build
	@./torrvia -check

clean:
	rm -f torrvia
