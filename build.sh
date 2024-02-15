#!/usr/bin/env bash
CGO_CFLAGS="-O2 -D__BLST_PORTABLE__"
export CGO_CFLAGS
COMMIT=$(git rev-parse --short HEAD)
TAG=${1:-dev}-$COMMIT
IMAGE=bloxroute/mev-relay-proxy-internal:$TAG

TOKEN=${2:$GITHUB_TOKEN}

if [ "$#" -eq 1 ]; then
  git tag $TAG
  git push git@github.com:bloXroute-Labs/mev-relay-proxy-internal.git $TAG --tag
fi

echo "Go build..."
GOOS=linux GOARCH=amd64 CGO_ENABLED=1 CC=x86_64-linux-musl-gcc  CXX=x86_64-linux-musl-g++  go build -o mev-relay-proxy-internal ./cmd/mev-relay-proxy
echo "Building container... $IMAGE With tag... $TAG"
docker build . -f Dockerfile --rm=true --platform linux/x86_64 --build-arg token=$TOKEN -t $IMAGE
docker tag bloxroute/mev-relay-proxy-internal:$TAG bloxroute/mev-relay-proxy-internal:$TAG
docker push bloxroute/mev-relay-proxy-internal:$TAG

rm mev-relay-proxy-internal
