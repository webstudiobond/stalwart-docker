FROM ghcr.io/stalwartlabs/cli:1.0.14@sha256:5b70732cb69cdab3ff0894c501544cf0bfa180fa75bcd0eeba4eebe04c1064e1 AS cli-donor

FROM ghcr.io/stalwartlabs/stalwart:v1.1.0-alpine@sha256:8988c12e4070b2d3b96b41478191df21d1a8a06702a249155815b1e497961526 AS stalwart-donor

FROM golang:1.27.3-alpine3.24@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS go-builder

WORKDIR /build

COPY go.mod ./
COPY init/ ./init/
COPY probe/ ./probe/

RUN mkdir -p /out/etc/ssl/certs /out/usr/local/bin && \
    chmod 0755 /out/etc /out/etc/ssl /out/etc/ssl/certs && \
    chmod 0755 /out/usr /out/usr/local /out/usr/local/bin && \
    cp /etc/ssl/certs/ca-certificates.crt /out/etc/ssl/certs/ca-certificates.crt && \
    chmod 0444 /out/etc/ssl/certs/ca-certificates.crt && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /out/usr/local/bin/init ./init && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /out/usr/local/bin/probe ./probe

FROM scratch AS cli

COPY --from=go-builder /out/etc /etc
COPY --from=cli-donor --chmod=0555 /usr/local/bin/stalwart-cli /usr/local/bin/stalwart-cli

ENTRYPOINT ["/usr/local/bin/stalwart-cli"]

FROM scratch AS server

COPY --from=go-builder /out/etc /etc
COPY --from=stalwart-donor --chmod=0555 /usr/local/bin/stalwart /usr/local/bin/stalwart
COPY --from=go-builder --chmod=0555 /out/usr/local/bin/init /usr/local/bin/init
COPY --from=go-builder --chmod=0555 /out/usr/local/bin/probe /usr/local/bin/probe

ENTRYPOINT ["/usr/local/bin/init"]
