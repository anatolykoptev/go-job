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

WORKDIR /app
COPY --from=builder /build/go_job .
EXPOSE 8891
CMD ["./go_job"]
