# --- Build stage ---
FROM golang:1.27-alpine AS build-stage

RUN apk add --no-cache git

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/coordinator ./cmd/coordinator
COPY internal ./internal

RUN mkdir -p /state && touch /state/.keep && \
    CGO_ENABLED=0 GOOS=linux go build -o /coordinator ./cmd/coordinator

FROM build-stage AS run-test-stage
RUN go test -v ./...

FROM alpine:3.22 AS build-release-stage

WORKDIR /

RUN apk add --no-cache ca-certificates git && \
    addgroup -g 65532 commitarium && \
    adduser -D -H -u 65532 -G commitarium -s /sbin/nologin commitarium

COPY --from=run-test-stage /coordinator /coordinator
COPY --chown=commitarium:commitarium --from=run-test-stage /state /var/lib/commitarium

RUN mkdir -p /workspaces && chown commitarium:commitarium /workspaces

EXPOSE 8080

USER commitarium:commitarium

ENTRYPOINT [ "/coordinator" ]
