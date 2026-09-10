# --- Build and verify the provider-neutral worker service ---
FROM golang:1.27-alpine AS build-stage

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN CGO_ENABLED=0 GOOS=linux go build -o /worker ./cmd/worker

FROM build-stage AS run-test-stage
RUN go test -v ./...

# Pin Claude Code so rebuilding cannot silently change the stream protocol
# underneath the tested Go adapter.
FROM node:22-bookworm-slim AS build-release-stage

ARG CLAUDE_CODE_VERSION=2.1.228

RUN apt-get update && \
    apt-get install --yes --no-install-recommends ca-certificates curl git jq && \
    rm -rf /var/lib/apt/lists/* && \
    npm install --global --omit=dev "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" && \
    claude --version && \
    npm cache clean --force

RUN groupadd --gid 65532 commitarium && \
    useradd --uid 65532 --gid 65532 --home-dir /var/lib/commitarium-provider \
        --no-create-home --shell /usr/sbin/nologin commitarium && \
    mkdir -p /var/lib/commitarium-provider /var/lib/commitarium-worker /workspaces /run/commitarium-agent && \
    chown -R commitarium:commitarium \
        /var/lib/commitarium-provider /var/lib/commitarium-worker /workspaces && \
    chmod 0700 /var/lib/commitarium-provider /var/lib/commitarium-worker

COPY --from=run-test-stage /worker /worker

ENV CLAUDE_CONFIG_DIR=/var/lib/commitarium-provider

EXPOSE 8081

HEALTHCHECK --interval=5s --timeout=3s --start-period=3s --retries=12 \
    CMD [ "/worker", "healthcheck" ]

USER commitarium:commitarium

ENTRYPOINT [ "/worker" ]
