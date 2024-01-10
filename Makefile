VERSION := $(shell git describe --tags --always)
APP := mev-relay-proxy
DOCKER_REPO :=bloxroute/${APP}-internal
MAIN_FILE := ./cmd/${APP}
COMMIT := $(shell git rev-parse --short HEAD)
TAG := beta-${TAG}-${COMMIT}

.PHONY: all
all: build

.PHONY: v
v:
	@echo "${VERSION}"

.PHONY: build
build:
	go build -ldflags "-X main._BuildVersion=${VERSION}" -v -o ${REPO} ${MAIN_FILE}

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
	GOOS=linux go build -ldflags "-X main._BuildVersion=${VERSION} -X main._BuildVersion=${VERSION}"  -v -o ${REPO} ${MAIN_FILE}

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


.PHONY: beta-tag-release
beta-tag-release: release
	git tag ${TAG}
  	git push git@github.com:bloXroute-Labs/${REPO}-internal.git ${TAG} --tag
	$(eval VERSION := $(shell git describe --tags --always))

.PHONY: release
release: build
	@echo "Building container... ${DOCKER_REPO}:${VERSION}"
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 CC=x86_64-linux-musl-gcc  CXX=x86_64-linux-musl-g++  go build -o ${APP}
	docker build . -f Dockerfile --rm=true --platform linux/x86_64 --build-arg token={token} -t ${RELAY_PROXY_IMAGE}
	docker tag ${APP}:latest ${DOCKER_REPO}:${VERSION}
	docker tag ${APP}:latest ${DOCKER_REPO}:latest
	docker push ${DOCKER_REPO}:${VERSION}
	docker push ${DOCKER_REPO}:latest
