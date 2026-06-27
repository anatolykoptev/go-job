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
RUN apk add --no-cache ca-certificates tzdata && \
    if [ "$WITH_PDF" = "1" ]; then \
        apk add --no-cache typst pandoc ghostscript qpdf; \
    fi

# Non-root user uid/gid 1001.
# Docker sets the process uid at container start — no setgroups(), no su-exec.
# This is compatible with no-new-privileges:true + cap_drop:ALL (unlike su-exec,
# which calls setgroups() and is EPERM under that security profile).
RUN addgroup -g 1001 -S appuser && \
    adduser -u 1001 -S -G appuser -H -D appuser

WORKDIR /app
COPY --from=builder /build/go_job .

# Pre-create /data/uploads (the named-volume mountpoint) and chown both
# /app and /data/uploads to uid 1001 BEFORE switching user.
# Docker copies the mountpoint dir's ownership into a NEW named volume on
# first start, so a fresh volume comes up 1001-owned without any runtime chown.
# Existing volumes require a one-time host-side chown (see PR body).
RUN mkdir -p /data/uploads && chown -R 1001:1001 /app /data/uploads

# go-twitter (and os.UserHomeDir) resolves $HOME for its session cache.
# With HOME=/tmp the sessions land on the already-mounted /tmp tmpfs —
# no extra writable path needed beyond what the compose already provides.
ENV HOME=/tmp

USER 1001

EXPOSE 8891
CMD ["./go_job"]
