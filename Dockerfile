FROM ghcr.io/stalwartlabs/cli:1.0.13@sha256:9a1e07307d6f890a193322c4b737fc0868405f0f8486a19922cbf362f43884fd AS cli-donor

FROM ghcr.io/stalwartlabs/stalwart:v0.16.27-alpine@sha256:c3e333637e56942ee47afb599dfc1d2e6fd2147260d2b1a49f12fda1fb684446 AS stalwart-donor

FROM golang:1.27.2-alpine3.24@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS go-builder

WORKDIR /build

COPY go.mod ./
COPY init/ ./init/
COPY probe/ ./probe/

RUN mkdir -p /out && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /out/init ./init && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /out/probe ./probe

FROM scratch AS cli

COPY --from=go-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=cli-donor --chmod=0555 /usr/local/bin/stalwart-cli /usr/local/bin/stalwart-cli

ENTRYPOINT ["/usr/local/bin/stalwart-cli"]

FROM scratch AS server

COPY --from=go-builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=stalwart-donor --chmod=0555 /usr/local/bin/stalwart /usr/local/bin/stalwart
COPY --from=go-builder --chmod=0555 /out/init /usr/local/bin/init
COPY --from=go-builder --chmod=0555 /out/probe /usr/local/bin/probe

ENTRYPOINT ["/usr/local/bin/init"]
