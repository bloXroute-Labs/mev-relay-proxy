# syntax=docker/dockerfile:1
FROM golang:1.20.10 as builder
ARG TOKEN
ARG VERSION
ARG APP
ARG MAIN_FILE_PATH
ARG SECRET_TOKEN

ENV GOPRIVATE=github.com/bloXroute-Labs/*
RUN git config --global url.https://$TOKEN@github.com/.insteadOf https://github.com/

WORKDIR /build

COPY go.mod ./
COPY go.sum ./

RUN go mod download
ADD . .
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -X main._BuildVersion=${VERSION} -X main._SecretToken=${SECRET_TOKEN}" -v -o ${APP} ./${MAIN_FILE_PATH}

FROM alpine

RUN apk add --no-cache libgcc libstdc++ libc6-compat
WORKDIR /app
COPY --from=builder /build/mev-relay-proxy /app/mev-relay-proxy
EXPOSE 18550
ENTRYPOINT ["/app/mev-relay-proxy"]
