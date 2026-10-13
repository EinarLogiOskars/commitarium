# --- Build stage ---
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build-stage

ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/marketplace-signup ./cmd/marketplace-signup

RUN go test ./cmd/marketplace-signup
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /marketplace-signup ./cmd/marketplace-signup

FROM alpine:3.22

RUN addgroup -g 65532 commitarium && \
    adduser -D -H -u 65532 -G commitarium -s /sbin/nologin commitarium

COPY --from=build-stage /marketplace-signup /marketplace-signup

USER commitarium
EXPOSE 8090
ENTRYPOINT ["/marketplace-signup"]
