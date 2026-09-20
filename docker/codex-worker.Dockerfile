# --- Build the Go worker service ---
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build-stage

ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN go test -v ./...
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /worker ./cmd/worker
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /commitarium-toolchain ./cmd/toolchain
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /commitarium-artifact ./cmd/artifact

# Keep the Codex version explicit so rebuilding the worker cannot silently
# change the provider protocol underneath the tested Go adapter.
FROM node:22-bookworm-slim AS build-release-stage

ARG CODEX_VERSION=0.153.4
ARG MISE_VERSION=2026.9.5

RUN apt-get update && \
    apt-get install --yes --no-install-recommends \
        build-essential \
        ca-certificates \
        curl \
        git \
        jq \
        openssh-client \
        pkg-config \
        python-is-python3 \
        python3 \
        python3-pip \
        python3-venv \
        ripgrep \
        unzip \
        zip && \
    rm -rf /var/lib/apt/lists/* && \
    npm install --global --omit=dev "@openai/codex@${CODEX_VERSION}" "@jdxcode/mise@${MISE_VERSION}" && \
    codex --version && \
    mise --version && \
    python --version && \
    python3 -m pip --version && \
    python3 -m venv /tmp/commitarium-python-smoke && \
    rm -rf /tmp/commitarium-python-smoke && \
    npm cache clean --force

RUN groupadd --gid 65532 commitarium && \
    useradd --uid 65532 --gid 65532 --home-dir /var/lib/commitarium-provider \
        --no-create-home --shell /usr/sbin/nologin commitarium && \
    mkdir -p /var/lib/commitarium-provider /var/lib/commitarium-worker /var/lib/commitarium-toolchains /workspaces /run/commitarium-agent && \
    chown -R commitarium:commitarium \
        /var/lib/commitarium-provider /var/lib/commitarium-worker /var/lib/commitarium-toolchains /workspaces && \
    chmod 0700 /var/lib/commitarium-provider /var/lib/commitarium-worker

COPY --from=build-stage /worker /worker
COPY --from=build-stage /commitarium-toolchain /usr/local/bin/commitarium-toolchain
COPY --from=build-stage /commitarium-artifact /usr/local/bin/commitarium-artifact

ENV CODEX_HOME=/var/lib/commitarium-provider

EXPOSE 8081

HEALTHCHECK --interval=5s --timeout=3s --start-period=3s --retries=12 \
    CMD [ "/worker", "healthcheck" ]

USER commitarium:commitarium

ENTRYPOINT [ "/worker" ]
