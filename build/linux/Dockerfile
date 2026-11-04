# Target-native GTK/C toolchain (amd64 or arm64 via binfmt/QEMU).
# Debian packages vary with security updates; Go and executable download helpers
# are checksum-verified. Ubuntu 24.04 is the oldest supported distribution: an
# AppImage must be built on it so that no bundled library needs a newer system
# library than it ships (scripts/check-appimage-abi.py enforces this), and the
# resulting packages require Ubuntu 24.04, Debian 13 or newer.
FROM ubuntu:24.04@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852
ARG TARGETARCH
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev libayatana-appindicator3-dev \
    curl ca-certificates git file patchelf librsvg2-bin dpkg-dev fakeroot python3 \
    squashfs-tools zstd xz-utils && rm -rf /var/lib/apt/lists/*
# Go's compiler/linker are static binaries. Run the builder-host compiler natively
# while targeting the container architecture; only C tools need QEMU on cross
# builds. This avoids emulating the large generated gotd Go compilation.
ARG BUILDARCH
RUN arch="${BUILDARCH:-$(dpkg --print-architecture)}"; \
    case "$arch" in \
      amd64) hash=ccfe7ae822d2c2f41f4fe7c956199967a4dac22d0ee0d2b2bfdb440aab3b20c5 ;; \
      arm64) hash=3f4227ce7b206a513e794f1393adb473a07b46e94d18bd15d1c684bbb7319797 ;; \
      *) exit 1 ;; \
    esac; \
    curl -fsSL "https://go.dev/dl/go1.26.10.linux-$arch.tar.gz" -o /go.tar.gz && \
    printf '%s  /go.tar.gz\n' "$hash" | sha256sum -c - && \
    tar -C /usr/local -xzf /go.tar.gz && rm /go.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}" GOFLAGS="-tags=gtk3" GOARCH="${TARGETARCH}" CC="gcc"
WORKDIR /src
