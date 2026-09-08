# --- Build stage ---
FROM golang:1.27-alpine AS build-stage

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/coordinator ./cmd/coordinator
COPY internal ./internal

RUN mkdir -p /state && touch /state/.keep && \
    CGO_ENABLED=0 GOOS=linux go build -o /coordinator ./cmd/coordinator

FROM build-stage AS run-test-stage
RUN go test -v ./...

FROM gcr.io/distroless/base-debian11 AS build-release-stage

WORKDIR /

COPY --from=run-test-stage /coordinator /coordinator
COPY --chown=nonroot:nonroot --from=run-test-stage /state /var/lib/commitarium

EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT [ "/coordinator" ]
