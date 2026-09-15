# --- Build stage ---
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build-stage

ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/coordinator ./cmd/coordinator
COPY internal ./internal

RUN go test -v ./...
RUN mkdir -p /state && touch /state/.keep && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /coordinator ./cmd/coordinator

FROM alpine:3.22 AS build-release-stage

WORKDIR /

RUN apk add --no-cache ca-certificates git && \
    addgroup -g 65532 commitarium && \
    adduser -D -H -u 65532 -G commitarium -s /sbin/nologin commitarium

COPY --from=build-stage /coordinator /coordinator
COPY --chown=commitarium:commitarium --from=build-stage /state /var/lib/commitarium

RUN mkdir -p /workspaces /var/lib/commitarium-toolchains && \
    chown commitarium:commitarium /workspaces /var/lib/commitarium-toolchains

EXPOSE 8080

USER commitarium:commitarium

ENTRYPOINT [ "/coordinator" ]
