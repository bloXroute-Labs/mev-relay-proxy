VERSION := $(shell git describe --tags --always)
APP := mev-relay-proxy
REPO := bloxroute/mev-relay-proxy-internal
DOCKER_REPO := bloxroute/mev-relay-proxy-internal
MAIN_FILE := ./cmd/${APP}
SECRET_TOKEN := 4nDpR2sVxYz1BtU6wFqGhJkLp3Tm5ZoX
.PHONY: all
all: build

.PHONY: v
v:
	@echo "${VERSION}"

.PHONY: build
build:
	go build -ldflags "-X main._BuildVersion=${VERSION} -X main._SecretToken=${SECRET_TOKEN}" -v -o ${REPO} ${MAIN_FILE}

.PHONY: test
test:
	go test ./...

.PHONY: test-race
test-race:
	go test -race ./...

.PHONY: fmt
fmt:
	gofmt -s -w .

.PHONY: lint
lint:
	go vet ./...
	staticcheck ./...

.PHONY: build-for-docker
build-for-docker:
	GOOS=linux GOARCH=amd64 go build -ldflags "-X main._BuildVersion=${VERSION}"  -v -o ${APP} ${MAIN_FILE}

.PHONY: docker-image
docker-image:
	DOCKER_BUILDKIT=1 docker build --progress=plain --platform linux/amd64 --build-arg APP_NAME=${APP_NAME} --build-arg SECRET_TOKEN=${SECRET_TOKEN} . -t ${APP}
	docker tag ${REPO}:latest ${DOCKER_REPO}:${VERSION}
	docker tag ${REPO}:latest ${DOCKER_REPO}:latest

.PHONY: docker-push
docker-push:
	docker push ${DOCKER_REPO}:${VERSION}
	docker push ${DOCKER_REPO}:latest

.PHONY: clean
clean:
	git clean -fdx
