VERSION := $(shell git describe --tags --always)
REPO := mev-relay-proxy
DOCKER_REPO :=bloxroute/${REPO}
MAIN_FILE := ./cmd/mev-relay-proxy
SECRET_TOKEN = "" #Add your secret token here

.PHONY: all
all: build

.PHONY: v
v:
	@echo "${VERSION}"

fmt:
	gofmt -s -w .

.PHONY: build
build:
	go build -ldflags "-X main._BuildVersion=${VERSION} -X main._SecretToken=${SECRET_TOKEN}" -v -o ${REPO} ${MAIN_FILE}

.PHONY: test
test:
	go test ./...

.PHONY: test-race
test-race:
	go test -race ./...

.PHONY: lint
lint:
	go vet ./...
	staticcheck ./...

.PHONY: build-for-docker
build-for-docker:
	GOOS=linux go build -ldflags "-X main._BuildVersion=${VERSION} -X main._SecretToken=${SECRET_TOKEN}" -v -o ${REPO} ${MAIN_FILE}

.PHONY: docker-image
docker-image:
	DOCKER_BUILDKIT=1 docker build . -t mev-relay-proxy --platform linux/x86_64
	docker tag ${REPO}:latest ${DOCKER_REPO}:${VERSION}
	docker tag ${REPO}:latest ${DOCKER_REPO}:latest

.PHONY: docker-push
docker-push:
	docker push ${DOCKER_REPO}:${VERSION}
	docker push ${DOCKER_REPO}:latest

.PHONY: clean
clean:
	git clean -fdx
