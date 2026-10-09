# Stage 1: Build the Go binary.
FROM golang:1.27.2-bookworm AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ENV CGO_ENABLED=0
ENV GOOS=linux
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=none
ARG DATE=unknown

# The version vars live in package main (cmd/geocoder-proxy),
# so they are set with -X main.<var>.
RUN GOARCH=$TARGETARCH go build \
    -ldflags="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o /app/geocoder-proxy ./cmd/geocoder-proxy

# Stage 2: Static runtime. The binary is the only thing that runs; the CA
# bundle is required because every upstream provider is reached over HTTPS.
FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/geocoder-proxy /geocoder-proxy

USER 65534
EXPOSE 8080

ENTRYPOINT ["/geocoder-proxy"]
