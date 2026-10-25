#!/usr/bin/env bash
# Installs what the demo needs beyond the image's features, then builds it, so the first
# `scripts/guard-demo.sh` starts at once.
set -euo pipefail
KIND_VERSION=v0.33.0
mkdir -p "$HOME/.local/bin"
export PATH="$HOME/.local/bin:$PATH"
case "$(uname -m)" in x86_64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) arch=$(uname -m) ;; esac
if ! command -v kind >/dev/null; then
  curl -fsSLo "$HOME/.local/bin/kind" "https://kind.sigs.k8s.io/dl/$KIND_VERSION/kind-linux-$arch"
  chmod +x "$HOME/.local/bin/kind"
fi
if ! command -v uv >/dev/null; then
  curl -LsSf https://astral.sh/uv/install.sh | env UV_NO_MODIFY_PATH=1 sh   # Python for the SDK
fi
scripts/guard-demo.sh setup
