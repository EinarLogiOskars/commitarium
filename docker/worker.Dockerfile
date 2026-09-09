# --- Build stage ---
FROM golang:1.27-alpine AS build-stage

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

RUN mkdir -p /state && touch /state/.keep && \
    CGO_ENABLED=0 GOOS=linux go build -o /worker ./cmd/worker

FROM build-stage AS run-test-stage
RUN go test -v ./...

FROM gcr.io/distroless/base-debian11 AS build-release-stage

WORKDIR /

COPY --from=run-test-stage /worker /worker
COPY --chown=nonroot:nonroot --from=run-test-stage /state /var/lib/commitarium-worker

EXPOSE 8081

HEALTHCHECK --interval=5s --timeout=3s --start-period=3s --retries=12 \
    CMD [ "/worker", "healthcheck" ]

USER nonroot:nonroot

ENTRYPOINT [ "/worker" ]
