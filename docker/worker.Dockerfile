# --- Build stage ---
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
RUN mkdir -p /state && touch /state/.keep && \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /worker ./cmd/worker

FROM gcr.io/distroless/base-debian11 AS build-release-stage

WORKDIR /

COPY --from=build-stage /worker /worker
COPY --chown=nonroot:nonroot --from=build-stage /state /var/lib/commitarium-worker

EXPOSE 8081

HEALTHCHECK --interval=5s --timeout=3s --start-period=3s --retries=12 \
    CMD [ "/worker", "healthcheck" ]

USER nonroot:nonroot

ENTRYPOINT [ "/worker" ]
