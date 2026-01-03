# Build stage
FROM golang:1.23-alpine AS builder

WORKDIR /app

# Install git for fetching dependencies
RUN apk add --no-cache git

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build the binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o /ts-discord-status ./cmd/ts-discord-status

# Runtime stage
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /ts-discord-status /ts-discord-status

ENTRYPOINT ["/ts-discord-status"]
