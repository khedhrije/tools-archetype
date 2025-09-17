# Build stage
FROM golang:1.24-alpine AS builder
WORKDIR /app

# Modules first for better caching
COPY go.mod ./
RUN go mod download

# Copy source (use . not ..)
COPY . .

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux go build -o server main.go

# Runtime image
FROM alpine:latest
WORKDIR /root/

# Copy binary AND static assets
COPY --from=builder /app/server .
COPY --from=builder /app/static ./static

EXPOSE 8080
CMD ["./server"]
