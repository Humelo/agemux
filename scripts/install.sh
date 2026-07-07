#!/usr/bin/env bash
set -euo pipefail

REPO="${AGEMUX_REPO:-Humelo/agemux}"
REF="${AGEMUX_REF:-v0.1.4}"
PREFIX="${AGEMUX_PREFIX:-$HOME/.local}"
BIN_DIR="${AGEMUX_BIN_DIR:-$PREFIX/bin}"
INSTALL_CLAUDE_SHIM=0
INSTALL_CODEX_LB=0
CODEX_LB_SPEC="${AGEMUX_CODEX_LB_SPEC:-codex-lb}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --prefix)
      [[ $# -ge 2 ]] || { echo "--prefix requires a value" >&2; exit 2; }
      PREFIX="$2"
      BIN_DIR="$PREFIX/bin"
      shift 2
      ;;
    --bin-dir)
      [[ $# -ge 2 ]] || { echo "--bin-dir requires a value" >&2; exit 2; }
      BIN_DIR="$2"
      shift 2
      ;;
    --install-claude-shim)
      INSTALL_CLAUDE_SHIM=1
      shift
      ;;
    --with-codex-lb)
      INSTALL_CODEX_LB=1
      shift
      ;;
    --no-codex-lb)
      INSTALL_CODEX_LB=0
      shift
      ;;
    --codex-lb-spec)
      [[ $# -ge 2 ]] || { echo "--codex-lb-spec requires a value" >&2; exit 2; }
      CODEX_LB_SPEC="$2"
      shift 2
      ;;
    -h|--help)
      cat <<'USAGE'
Usage: install.sh [--prefix DIR] [--bin-dir DIR] [--install-claude-shim] [--with-codex-lb] [--no-codex-lb] [--codex-lb-spec SPEC]
USAGE
      exit 0
      ;;
    *)
      echo "unknown option: $1" >&2
      exit 2
      ;;
  esac
done

need() {
  command -v "$1" >/dev/null 2>&1 || {
    echo "missing required command: $1" >&2
    exit 1
  }
}

need curl
need tar

install_uv_if_missing() {
  if command -v uv >/dev/null 2>&1; then
    return
  fi
  echo "Installing uv for codex-lb..."
  curl -LsSf https://astral.sh/uv/install.sh | sh
  export PATH="$HOME/.local/bin:$HOME/.cargo/bin:$PATH"
  command -v uv >/dev/null 2>&1 || {
    echo "uv install finished, but uv is not on PATH. Add ~/.local/bin to PATH and rerun." >&2
    exit 1
  }
}

mkdir -p "$BIN_DIR"

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

os_name="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os_name" in
  linux) os_name="linux" ;;
  darwin) os_name="darwin" ;;
  *) echo "unsupported OS: $os_name" >&2; exit 1 ;;
esac

arch_name="$(uname -m)"
case "$arch_name" in
  x86_64|amd64) arch_name="amd64" ;;
  arm64|aarch64) arch_name="arm64" ;;
  *) echo "unsupported architecture: $arch_name" >&2; exit 1 ;;
esac

version="${REF#v}"
asset="agemux_${version}_${os_name}_${arch_name}.tar.gz"
asset_url="https://github.com/$REPO/releases/download/$REF/$asset"

echo "Downloading Agent Multiplexer $REF for $os_name/$arch_name..."
curl -fsSL "$asset_url" -o "$tmp_dir/$asset"
tar -xzf "$tmp_dir/$asset" -C "$tmp_dir"

[[ -x "$tmp_dir/agemux" ]] || { echo "release asset missing executable: agemux" >&2; exit 1; }
install -m 0755 "$tmp_dir/agemux" "$BIN_DIR/agemux"

if [[ "$INSTALL_CLAUDE_SHIM" == "1" ]]; then
  "$BIN_DIR/agemux" claude-accounts install-shim --force --bin-dir "$BIN_DIR"
fi

if [[ "$INSTALL_CODEX_LB" == "1" ]]; then
  install_uv_if_missing
  echo "Installing latest codex-lb via uv..."
  uv tool install --upgrade --force "$CODEX_LB_SPEC"
  UV_TOOL_BIN="$(uv tool dir --bin)"
  if [[ -n "$UV_TOOL_BIN" ]]; then
    export PATH="$UV_TOOL_BIN:$PATH"
  fi
else
  UV_TOOL_BIN=""
fi

if ! command -v shpool >/dev/null 2>&1; then
  echo "Note: agemux requires shpool. Install shpool to use persistent agent sessions." >&2
fi

cat <<EOF
Installed Agent Multiplexer:
  $BIN_DIR/agemux
  codex-lb: $(if [[ "$INSTALL_CODEX_LB" == "1" ]]; then command -v codex-lb || printf 'installed by uv; ensure ~/.local/bin is on PATH'; else printf 'not installed by agemux installer; pass --with-codex-lb to opt in'; fi)

Make sure this directory is on PATH:
  export PATH="$BIN_DIR${UV_TOOL_BIN:+:$UV_TOOL_BIN}:\$PATH"
EOF
