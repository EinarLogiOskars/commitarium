# --- Build stage ---
FROM golang:1.27-alpine AS build-stage

WORKDIR /app

COPY go.mod ./
RUN go mod download

COPY cmd/coordinator ./cmd/coordinator

RUN CGO_ENABLED=0 GOOS=linux go build -o /coordinator ./cmd/coordinator

FROM build-stage AS run-test-stage
RUN go test -v ./...

FROM gcr.io/distroless/base-debian11 AS build-release-stage

WORKDIR /

COPY --from=run-test-stage /coordinator /coordinator

EXPOSE 8080

USER nonroot:nonroot

ENTRYPOINT [ "/coordinator" ]