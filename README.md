# mev-relay-proxy

`mev-relay-proxy` allows validators to connect to mev boost relay via grpc.

---

# grpc support
`mev-relay-proxy` server proto files are defined as part of `github.com/bloXroute-Labs/relay-grpc`

# Installation

`mev-relay-proxy` can run in any machine, as long as it is reachable by the validator client. The default port is 18550. The most common setup is to install it in the same machine as the validator client.

## From source

Install the dependencies:

- [Install Go 1.20](https://go.dev/doc/install)
- Install build dependencies (in Ubuntu):

```bash
sudo apt install make gcc
```

# Build:

```bash
make build
```

# Run:

## MEV Relay Proxy Docker Setup

This guide provides instructions on how to run the `mev-relay-proxy` as Docker container.
a
## Prerequisites

- [Docker](https://www.docker.com/get-started) installed on your machine.

## Steps

### 1. Stop and Remove Existing Container

```bash
docker stop mev-relay-proxy
docker rm mev-relay-proxy
```

### 2. Run MEV Relay Proxy Container
```bash
docker run --name=mev-relay-proxy \
--hostname=0ba39b5ff5c5 \
--workdir=/app \
-p 18550:18550 \
--restart=no \
--runtime=runc \
--detach=true \
bloxroute/mev-relay-proxy:v0.0.1 \
--addr 0.0.0.0:18551 \
--relay {RELAY_IP:PORT}
```
- Replace RELAY_IP:PORT with the desired relay address.
- The MEV Relay Proxy should now be running and accessible at http://localhost:18551.

### 3. Configure MEV Boost to connect to mev-relay-proxy
-  connect to mev relay proxy by configuring the relay proxy ip using `--relay` flag in mev boost

# How to configure relay proxy to MEV Boost
## Goerli testnet

Run MEV-Boost pointed at a Goerli relay and relay-proxy:

``./mev-boost -goerli -relay-check -relays http://0x821f2a65afb70e7f2e820a925a9b4c80a159620582c1766b1b09729fec178b11ea22abb3a51f07b288be815a1a2ff516@bloxroute.max-profit.builder.goerli.blxrbdn.com,http://0x821f2a65afb70e7f2e820a925a9b4c80a159620582c1766b1b09729fec178b11ea22abb3a51f07b288be815a1a2ff516@54.164.127.218:18551 &
``
## Mainnet
### Max-profit
Run MEV-Boost pointed at a mainnet max profit relay and relay-proxy running as local host:

``./mev-boost -relay-check -relays https://0x8b5d2e73e2a3a55c6c87b8b6eb92e0149a125c852751db1422fa951e42a09b82c142c3ea98d0d9930b056a3bc9896b8f@bloxroute.max-profit.blxrbdn.com,https://0x8b5d2e73e2a3a55c6c87b8b6eb92e0149a125c852751db1422fa951e42a09b82c142c3ea98d0d9930b056a3bc9896b8f@relay-proxy.blxrbdn.com:18550 &
``
### Regulated
Run MEV-Boost pointed at a mainnet regulated relay and relay-proxy running as local host:

``./mev-boost -relay-check -relays https://0xb0b07cd0abef743db4260b0ed50619cf6ad4d82064cb4fbec9d3ec530f7c5e6793d9f286c4e082c0244ffb9f2658fe88@bloxroute.regulated.blxrbdn.com,https://0xb0b07cd0abef743db4260b0ed50619cf6ad4d82064cb4fbec9d3ec530f7c5e6793d9f286c4e082c0244ffb9f2658fe88@relay-proxy.blxrbdn.com:18551 &
``
