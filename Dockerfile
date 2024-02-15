# syntax=docker/dockerfile:1
FROM golang:1.21.3 as builder
ARG token
ARG version

ENV GOPRIVATE=github.com/bloXroute-Labs/*
ENV GOOS=linux
RUN git config --global url.https://$token@github.com/.insteadOf https://github.com/

WORKDIR /build
COPY . /build/

FROM alpine

RUN apk add --no-cache libgcc libstdc++ libc6-compat
WORKDIR /app
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /build/mev-relay-proxy-internal /app/mev-relay-proxy-internal
EXPOSE 18550
ENTRYPOINT ["/app/mev-relay-proxy-internal"]
