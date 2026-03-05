FROM golang:alpine AS builder
WORKDIR /build
COPY . .
RUN VERSION=$(git describe --tags --always 2>/dev/null || echo "dev") && \
    CGO_ENABLED=0 go build -mod=vendor -ldflags="-s -w -X main.version=${VERSION}" -o go_job .

FROM alpine:latest
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /build/go_job .
EXPOSE 8891
CMD ["./go_job"]
