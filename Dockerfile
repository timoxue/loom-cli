FROM golang:1.24-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o loom ./cmd

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S loom && adduser -S -G loom loom
WORKDIR /loom
COPY --from=builder /build/loom ./loom
RUN chown -R loom:loom /loom

USER loom

# /loom/skills      — mount your .loom.json skill files here (read-only)
# /home/loom/.loom  — receipt cache (persist across restarts)
VOLUME ["/loom/skills", "/home/loom/.loom"]

EXPOSE 8080
ENTRYPOINT ["./loom"]
CMD ["serve", "--skills-dir", "/loom/skills", "--port", "8080"]
