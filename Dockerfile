FROM ghcr.io/stalwartlabs/cli:1.0.13@sha256:9a1e07307d6f890a193322c4b737fc0868405f0f8486a19922cbf362f43884fd AS cli-donor

FROM ghcr.io/stalwartlabs/stalwart:v1.0.0-alpine@sha256:d870b94369777d1bfa27dff509de724410fc1f1cef41d131e0fd4db44640f228 AS stalwart-donor

FROM golang:1.27.3-alpine3.24@sha256:b8c9b82affbbb82a020bbcf998dad2ba9516944ac7e883524642a481a9505e9c AS go-builder

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
