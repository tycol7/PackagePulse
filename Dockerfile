# Stage 1: Build the Go binary
FROM golang:1.26-alpine AS builder

WORKDIR /app

# Install CA certificates for HTTPS calls (Agent Platform, Google APIs)
RUN apk add --no-cache ca-certificates git

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Compile static Go binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o server ./cmd/server

# Stage 2: Distroless Google Minimal Runtime
FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /app/server /server

USER nonroot:nonroot

EXPOSE 8080

ENTRYPOINT ["/server"]
