VERSION := $(shell git describe --tags --always)
APP := mev-relay-proxy
DOCKER_REPO :=bloxroute/${APP}
MAIN_FILE := ./cmd/${APP}
COMMIT := $(shell git rev-parse --short HEAD)
TAG := ${TAG}
BETA_TAG := beta-${TAG}-${COMMIT}

.PHONY: all
all: build

.PHONY: v
v:
	@echo "${VERSION}"

.PHONY: build
build:
	GOOS=linux CGO_ENABLED=1 CC=x86_64-linux-musl-gcc  CXX=x86_64-linux-musl-g++ go build -ldflags "-X main._BuildVersion=${VERSION}  -X main._AppName=${APP} -X main._TOKEN=${TOKEN}" -v -o ${APP} ${MAIN_FILE}

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
	GOOS=linux CGO_ENABLED=1 CC=x86_64-linux-musl-gcc  CXX=x86_64-linux-musl-g++ go build -ldflags "-X main._BuildVersion=${VERSION} -X main._BuildVersion=${VERSION}"  -v -o ${REPO} ${MAIN_FILE}

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
beta-tag-release: build release
	git tag ${BETA_TAG}
  	git push git@github.com:bloXroute-Labs/${REPO}-internal.git ${BETA_TAG} --tag
	$(eval VERSION := $(shell git describe --tags --always))

.PHONY: tag-release
beta-tag-release: build release
	git tag ${TAG}
  	git push git@github.com:bloXroute-Labs/${REPO}-internal.git ${TAG} --tag
	$(eval VERSION := $(shell git describe --tags --always))


.PHONY: release
release: build
	@echo "Building container... ${DOCKER_REPO}:${VERSION}"
	docker build . -f Dockerfile --rm=true --platform linux/x86_64 --build-arg token={token} -t ${APP}
	docker tag ${APP}:latest ${DOCKER_REPO}:${VERSION}
	docker tag ${APP}:latest ${DOCKER_REPO}:latest
	docker push ${DOCKER_REPO}:${VERSION}
	docker push ${DOCKER_REPO}:latest
