FROM ghcr.io/stalwartlabs/cli:1.0.13@sha256:9a1e07307d6f890a193322c4b737fc0868405f0f8486a19922cbf362f43884fd AS cli-donor

FROM ghcr.io/stalwartlabs/stalwart:v0.16.24-alpine@sha256:dcff88258bf65d29ab9fc424d1006c9bda7290577151488adf2a47718d736977 AS stalwart-donor

FROM golang:1.27.1-alpine3.24@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS go-builder

WORKDIR /build

COPY go.mod ./
COPY init/ ./init/
COPY probe/ ./probe/

RUN mkdir -p /out && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /out/init ./init && \
    CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /out/probe ./probe

FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS certs

ARG APP_UID=2000
ARG APP_GID=2000

# hadolint ignore=DL3018
RUN apk add --no-cache ca-certificates && \
    mkdir -p /out/etc && \
    printf "stalwart:x:%s:%s::/nonexistent:/sbin/nologin\n" "${APP_UID}" "${APP_GID}" > /out/etc/passwd && \
    printf "stalwart:x:%s:\n" "${APP_GID}" > /out/etc/group

FROM scratch AS cli

ARG APP_UID=2000
ARG APP_GID=2000

LABEL maintainer="underhax" \
      description="Hardened, zero-shell Stalwart CLI runtime featuring unprivileged execution"

COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=certs /out/etc/passwd /etc/passwd
COPY --from=certs /out/etc/group /etc/group
COPY --from=cli-donor --chmod=0555 /usr/local/bin/stalwart-cli /usr/local/bin/stalwart-cli

USER ${APP_UID}:${APP_GID}

ENTRYPOINT ["/usr/local/bin/stalwart-cli"]

FROM scratch AS server

ARG APP_UID=2000
ARG APP_GID=2000

LABEL maintainer="underhax" \
      description="Hardened, zero-shell Stalwart Mail Server runtime featuring UNIX domain socket IPC, DNS-01 ACME automation, and unprivileged execution"

COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=certs /out/etc/passwd /etc/passwd
COPY --from=certs /out/etc/group /etc/group
COPY --from=stalwart-donor --chmod=0555 /usr/local/bin/stalwart /usr/local/bin/stalwart
COPY --from=go-builder --chmod=0555 /out/init /usr/local/bin/init
COPY --from=go-builder --chmod=0555 /out/probe /usr/local/bin/probe

USER ${APP_UID}:${APP_GID}

ENTRYPOINT ["/usr/local/bin/init"]
