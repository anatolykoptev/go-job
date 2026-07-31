# fonts stage: install IBM Plex Sans + Mono TTFs when WITH_PDF=1.
#
# Typst substitutes a missing font family SILENTLY — no build-time error, no
# run-time error, no metric. The rendered PDF still looks like a resume, just
# in the wrong font (Libertinus Serif on a bare alpine/typst image). Shipping
# the font the theme names is the only way to get the measured design into the
# container.
#
# Ubuntu is used because it carries fonts-ibm-plex 6.1.1-1 (the same build
# measured on the host). Debian trixie does not carry the package; Alpine has
# no IBM Plex Sans package at all. The glob pair matches 32 files / 5.3 MB and
# excludes Arabic/Thai/Hebrew/Devanagari/Condensed/Var families — do not widen
# it to IBMPlexSans* (the missing hyphen pulls in ~70 MB of scripts nobody
# renders). Typst finds fonts under /usr/share/fonts/** on its own; no
# fontconfig, no fc-cache, no TYPST_FONT_PATHS needed.
FROM ubuntu:24.04 AS fonts
ARG WITH_PDF=0
RUN mkdir -p /out && \
    if [ "$WITH_PDF" = "1" ]; then \
        apt-get update && \
        apt-get install -y --no-install-recommends fonts-ibm-plex && \
        cp /usr/share/fonts/truetype/ibm-plex/IBMPlexSans-*.ttf \
           /usr/share/fonts/truetype/ibm-plex/IBMPlexMono-*.ttf /out/ && \
        rm -rf /var/lib/apt/lists/*; \
    fi

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

# Copy IBM Plex fonts into the runtime image. /out is always created by the
# fonts stage (even when WITH_PDF=0), so this COPY succeeds unconditionally.
# When WITH_PDF=0 the directory is empty — zero bytes added to the slim image.
COPY --from=fonts /out/ /usr/share/fonts/truetype/ibm-plex/

USER 1001

EXPOSE 8891
CMD ["./go_job"]
