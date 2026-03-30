# ── args ──────────────────────────────────────────────────────────────────────
ARG FLUTTER_VERSION=3.38.5

# ── iosbox binary ─────────────────────────────────────────────────────────────
FROM ubuntu:24.04 AS builder

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y \
    curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

COPY go.mod go.sum* /workspace/
COPY cmd/ /workspace/cmd/
COPY internal/ /workspace/internal/

RUN ARCH=$(uname -m) && \
    case "$ARCH" in \
      x86_64)  GOARCH=amd64 ;; \
      aarch64) GOARCH=arm64 ;; \
      *) echo "Unsupported arch: $ARCH" && exit 1 ;; \
    esac && \
    curl -sL "https://go.dev/dl/go1.25.3.linux-${GOARCH}.tar.gz" | tar xz -C /usr/local
ENV PATH="/usr/local/go/bin:${PATH}"
RUN go build -o /iosbox ./cmd/iosbox/

# ── runtime ───────────────────────────────────────────────────────────────────
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y \
    curl git unzip xz-utils zip clang lld cmake ninja-build pkg-config \
    libgtk-3-dev liblzma-dev libstdc++-12-dev \
    binutils python3 rsync cpio \
    && rm -rf /var/lib/apt/lists/*

RUN ARCH=$(uname -m) && \
    case "$ARCH" in \
      x86_64)  SWIFT_DIR=ubuntu2404;          SWIFT_SUFFIX="" ;; \
      aarch64) SWIFT_DIR=ubuntu2404-aarch64;  SWIFT_SUFFIX="-aarch64" ;; \
      *) echo "Unsupported arch: $ARCH" && exit 1 ;; \
    esac && \
    curl -sL "https://download.swift.org/swift-6.2-release/${SWIFT_DIR}/swift-6.2-RELEASE/swift-6.2-RELEASE-ubuntu24.04${SWIFT_SUFFIX}.tar.gz" \
    | tar xz --strip-components=1 -C /

ARG FLUTTER_VERSION
RUN git clone --depth 1 --branch ${FLUTTER_VERSION} https://github.com/flutter/flutter.git /opt/flutter
ENV PATH="/opt/flutter/bin:/opt/flutter/bin/cache/dart-sdk/bin:${PATH}"
RUN flutter precache --ios
RUN flutter config --enable-swift-package-manager

WORKDIR /workspace

COPY --from=builder /iosbox /usr/local/bin/iosbox
COPY shims/ /usr/local/lib/iosbox/shims/

CMD ["bash"]
