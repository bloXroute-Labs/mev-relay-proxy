VERSION := $(shell git describe --tags --always)
APP := mev-relay-proxy
DOCKER_REPO :=
MAIN_FILE := ./cmd/${APP}
.PHONY: all
all: build

.PHONY: v
v:
	@echo "${VERSION}"

.PHONY: build
build:
	go build -ldflags "-X main._BuildVersion=${VERSION}" -v -o ${APP} ${MAIN_FILE}

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
	GOOS=linux go build -ldflags "-X main._BuildVersion=${VERSION}"  -v -o ${APP} ${MAIN_FILE}

.PHONY: docker-image
docker-image:
	DOCKER_BUILDKIT=1 docker build --progress=plain --platform linux/x86_64  --build-arg APP_NAME=${APP_NAME} . -t ${APP}
	docker tag ${REPO}:latest ${DOCKER_REPO}:${VERSION}
	docker tag ${REPO}:latest ${DOCKER_REPO}:latest

.PHONY: docker-push
docker-push:
	docker push ${DOCKER_REPO}:${VERSION}
	docker push ${DOCKER_REPO}:latest

.PHONY: clean
clean:
	git clean -fdx
