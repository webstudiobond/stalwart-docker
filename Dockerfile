FROM ghcr.io/stalwartlabs/cli:2.0.0@sha256:8588aa3ea88ed740f83292ab37760869541df3da39cd1c8e6494fb43ece888c9 AS cli-donor

FROM ghcr.io/stalwartlabs/stalwart:v1.2.0-alpine@sha256:2b7dfee66dfd116a903607826e03e4f8af8841207fae28098aa908c568ca919a AS stalwart-donor

FROM golang:1.27.4-alpine3.24@sha256:70bd7ddea8b6cbe5d6943cde279d6b67feb10cc924fb9100d1d8757c1575b280 AS go-builder

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
