FROM --platform=${BUILDPLATFORM:-linux/amd64} golang:1.26-alpine AS builder

ARG TARGETPLATFORM
ARG BUILDPLATFORM
ARG TARGETOS
ARG TARGETARCH
ARG GIT_TAG
ARG GIT_COMMIT
ARG USERNAME=vlv-code
ARG REPOSITORY_NAME=xray-checker-tg-bot

ENV CGO_ENABLED=0
ENV GO111MODULE=on

# Install UPX for binary compression
RUN apk add --no-cache upx

WORKDIR /go/src/github.com/${USERNAME}/${REPOSITORY_NAME}

COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY . .

RUN CGO_ENABLED=${CGO_ENABLED} GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
  go build -ldflags="-s -w -X main.version=${GIT_TAG} -X main.commit=${GIT_COMMIT}" -a -installsuffix cgo -o /usr/bin/xray-checker . && \
  upx --best --lzma /usr/bin/xray-checker

FROM alpine:3.21

ARG USERNAME=vlv-code
ARG REPOSITORY_NAME=xray-checker-tg-bot
ARG WEB_ENABLED=true

LABEL org.opencontainers.image.source=https://github.com/${USERNAME}/${REPOSITORY_NAME}

# su-exec is used by the entrypoint to drop privileges after fixing
# ownership of bind-mounted volumes (Docker creates them as root:root,
# which would crash-loop the unprivileged app on fresh deployments).
RUN apk add --no-cache ca-certificates curl tzdata su-exec && \
    adduser -D -u 1000 appuser && \
    mkdir -p /app/geo /app/data && \
    chown -R appuser:appuser /app

WORKDIR /app
COPY --from=builder /usr/bin/xray-checker /usr/bin/xray-checker
COPY entrypoint.sh /entrypoint.sh

ENV WEB_ENABLED=${WEB_ENABLED}

# The container intentionally starts as root so the entrypoint can chown
# the mounted volumes (/app/data, /app/geo) and then drop privileges to
# appuser (uid 1000) via su-exec before exec'ing the checker. Override with
# `user:` in compose or `--user` to run unprivileged from the start; in that
# case the app degrades gracefully (geo download and persistence become
# best-effort with warnings).
ENTRYPOINT ["/entrypoint.sh"]
CMD ["/usr/bin/xray-checker"]
