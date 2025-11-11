# Build stage
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy go mod files
COPY ./go.mod ./go.sum ./
RUN cat go.mod
RUN go mod download

# Copy source code
COPY . .
# Build the demo application
WORKDIR /app/
RUN CGO_ENABLED=0 GOOS=linux go build -o /app/demo-server ./demo

# Runtime stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

# Copy the binary
COPY --from=builder /app/demo-server .

EXPOSE 8081

CMD ["./demo-server"]
