#!/usr/bin/env bash
# Immutable build inputs. Update URLs and digests together after independent
# verification; do not override these values from the release environment.

WHISPER_VERSION="1.9.5"
WHISPER_SOURCE_URL="https://codeload.github.com/ggml-org/whisper.cpp/tar.gz/refs/tags/v1.9.5"
WHISPER_SOURCE_SHA256="ff1a9053feb509ff9d7729703355541ae9690073a6b1c40eb692c962e0dc1720"
# v1.9.5 and build b5454 are the same upstream commit
# d1be6fde11ac6e0407606b4e42fe72d34add8037.
WHISPER_WINDOWS_RELEASE="b5454"
WHISPER_WINDOWS_CPU_ASSET="whisper-bin-x64.zip"
WHISPER_WINDOWS_CPU_SHA256="6ba69e3482d7826214f90a6a9c84ca07782aec1e1d0c6a7c30c994fd5d816ccb"
WHISPER_WINDOWS_CUDA_ASSET="whisper-bin-win-cuda-12.4.0-x64.zip"
WHISPER_WINDOWS_CUDA_SHA256="afef0b881c500958921c3f5523b50e59ee2ec9b6f5cbd25b324c51ed308a957a"
CUDA_12_4_EULA_URL="https://docs.nvidia.com/cuda/archive/12.4.0/pdf/EULA.pdf"
CUDA_12_4_EULA_SHA256="6ada441ae5a45a4a3d51ade8850fa2229fc6dd95de0c9da6e2cdd7a46701b844"

FFMPEG_VERSION="7.1.5"
FFMPEG_SOURCE_URL="https://ffmpeg.org/releases/ffmpeg-7.1.5.tar.xz"
FFMPEG_SOURCE_SHA256="de668509caf9e35e3cd162473441fdb29538c6d96ed080292b3cf9e6fc5d558f"
FFMPEG_SIGNATURE_URL="https://ffmpeg.org/releases/ffmpeg-7.1.5.tar.xz.asc"
FFMPEG_SIGNATURE_SHA256="7ce4b9d56e3ef0cd4c3a9c0b2c8a034ac91e6c4c011c1f10b05a8aa7292fca35"
FFMPEG_SIGNING_FINGERPRINT="FCF986EA15E6E293A5644F10B4322F04D67658D8"

# Microsoft Evergreen bootstrapper. The immutable delivery URL and digest pin
# the bootstrapper bytes; Evergreen intentionally installs Microsoft's current
# supported runtime rather than a fixed runtime version.
WEBVIEW2_BOOTSTRAPPER_URL="https://msedge.sf.dl.delivery.mp.microsoft.com/filestreamingservice/files/068be74d-a831-494f-8f57-c173756d5a33/MicrosoftEdgeWebview2Setup.exe"
WEBVIEW2_BOOTSTRAPPER_SHA256="cf2f2445fef0c5c14e372045f84998771eeb8bb01d59be4afd00692fa160082e"
# SHA-256 of the DER-encoded Microsoft Corporation leaf signing certificate.
WEBVIEW2_SIGNER_CERT_SHA256="cb97e8e85e8e9321fb2646e9574efd17669b3b0581d24262ac7c8a227433a244"
