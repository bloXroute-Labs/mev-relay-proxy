# syntax=docker/dockerfile:1
FROM golang:1.20.10 as builder
ARG TOKEN
ARG VERSION
ARG APP
ARG MAIN_FILE_PATH
ENV GOOS=linux

ENV GOPRIVATE=github.com/bloXroute-Labs/*
RUN git config --global url.https://$TOKEN@github.com/.insteadOf https://github.com/

WORKDIR /build
COPY . /build/

FROM alpine

RUN apk add --no-cache libgcc libstdc++ libc6-compat
WORKDIR /app
COPY --from=builder /build/mev-relay-proxy /app/mev-relay-proxy
EXPOSE 18550
ENTRYPOINT ["/app/mev-relay-proxy"]