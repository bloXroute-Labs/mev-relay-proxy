#!/usr/bin/env bash
VERSION=${1:-latest}
APP=mev-relay-proxy
MAIN_FILE_PATH=cmd/${APP}
IMAGE="bloxroute/${APP}-internal:${VERSION}"

echo "Building container... $IMAGE"
docker build . -f Dockerfile --rm=true --platform linux/x86_64 --build-arg TOKEN=${2:-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} --build-arg VERSION=${VERSION} --build-arg APP=${APP} --build-arg MAIN_FILE_PATH=${MAIN_FILE_PATH} -t $IMAGE
docker tag "$IMAGE" bloxroute/${APP}-internal:${VERSION}
docker push $IMAGE
