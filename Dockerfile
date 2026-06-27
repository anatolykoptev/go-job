FROM golang:alpine AS builder
WORKDIR /build

COPY go.mod go.sum ./
COPY vendor/ vendor/

COPY . .
RUN VERSION=$(git describe --tags --always 2>/dev/null || echo "dev") && \
    CGO_ENABLED=0 go build -mod=vendor -ldflags="-s -w -X main.version=${VERSION}" -o go_job .

FROM alpine:latest

# WITH_PDF=1 installs the Typst PDF pipeline (pandoc + typst + ghostscript + qpdf).
# The resulting image is ~200 MB larger. Default (WITH_PDF=0) stays slim.
# Build-time: docker build --build-arg WITH_PDF=1 .
# Runtime: no extra config — pdfrender.TypstAdapter auto-detects binary availability.
ARG WITH_PDF=0
RUN apk add --no-cache ca-certificates tzdata su-exec && \
    if [ "$WITH_PDF" = "1" ]; then \
        apk add --no-cache typst pandoc ghostscript qpdf; \
    fi

# Non-root user. uid/gid 1001 avoids collision with common host users.
# The entrypoint runs as root briefly to fix upload-volume ownership, then
# execs as appuser — so the long-running process is never root.
RUN addgroup -S -g 1001 appuser && \
    adduser -S -u 1001 -G appuser appuser

# Pre-create the uploads mount point with correct ownership so that a fresh
# named volume (which Docker initialises from the image directory) is writable
# by appuser without any chown step.
RUN mkdir -p /data/uploads && chown appuser:appuser /data/uploads

WORKDIR /app
COPY --from=builder /build/go_job .
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

# HOME=/tmp: go-twitter caches OAuth sessions under $HOME/.go-twitter/sessions.
# /tmp is already a tmpfs in the standard compose setup, so sessions are
# ephemeral by design. This avoids needing a separate tmpfs for /root/.go-twitter
# and is compatible with the read_only:true rootfs (only /tmp and /data/uploads
# are writable at runtime).
ENV HOME=/tmp

EXPOSE 8891
ENTRYPOINT ["/entrypoint.sh"]
CMD ["./go_job"]
