#!/usr/bin/env bash
# Download yt-dlp + static ffmpeg/ffprobe into resources/<platform>/bin/
# Usage:
#   ./scripts/fetch-tools.sh                  # host platform only
#   ./scripts/fetch-tools.sh all              # every platform (for CI packaging)
#   ./scripts/fetch-tools.sh windows-amd64    # one platform
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RES="$ROOT/resources"

# Pin versions for reproducible builds (bump when you want updates)
YTDLP_TAG="${YTDLP_TAG:-latest}"
# BtbN builds track ffmpeg master; use a known-good release asset name pattern
FFMPEG_RELEASE="${FFMPEG_RELEASE:-latest}"

fetch() {
  local url="$1" out="$2"
  echo "  ← $url"
  curl -fsSL --retry 3 -o "$out" "$url"
}

chmod_bin() {
  local f="$1"
  [ -f "$f" ] || return 0
  case "$f" in
    *.exe) ;;
    *) chmod +x "$f" ;;
  esac
}

# --- yt-dlp (single-file binaries from GitHub releases) ---
# https://github.com/yt-dlp/yt-dlp/releases
ytdlp_url() {
  local plat="$1"
  local base="https://github.com/yt-dlp/yt-dlp/releases/${YTDLP_TAG}/download"
  case "$plat" in
    windows-amd64) echo "$base/yt-dlp.exe" ;;
    windows-arm64) echo "$base/yt-dlp.exe" ;; # official win is amd64; arm64 may use same or py
    linux-amd64)   echo "$base/yt-dlp_linux" ;;
    linux-arm64)   echo "$base/yt-dlp_linux_aarch64" ;;
    darwin-amd64)  echo "$base/yt-dlp_macos" ;;
    darwin-arm64)  echo "$base/yt-dlp_macos" ;; # universal-ish; arm works with macos build
    *) echo ""; return 1 ;;
  esac
}

# --- ffmpeg static builds ---
# Windows/Linux: BtbN/FFmpeg-Builds  https://github.com/BtbN/FFmpeg-Builds/releases
# macOS: evermeet.cx style is x86-only; use osxexperts or homebrew-export —
#   for CI we use https://github.com/eugeneware/ffmpeg-static style mirrors or
#   https://github.com/yt-dlp/FFmpeg-Builds
#
# Practical approach: yt-dlp's recommended static builds
# https://github.com/yt-dlp/FFmpeg-Builds/releases

ffmpeg_archive_url() {
  local plat="$1"
  local base="https://github.com/yt-dlp/FFmpeg-Builds/releases/download/latest"
  case "$plat" in
    windows-amd64) echo "$base/ffmpeg-master-latest-win64-gpl.zip" ;;
    windows-arm64) echo "$base/ffmpeg-master-latest-winarm64-gpl.zip" ;;
    linux-amd64)   echo "$base/ffmpeg-master-latest-linux64-gpl.tar.xz" ;;
    linux-arm64)   echo "$base/ffmpeg-master-latest-linuxarm64-gpl.tar.xz" ;;
    # macOS: no official yt-dlp FFmpeg-Builds for darwin in all tags; use evermeet for amd64
    # and leave arm64 to PATH/homebrew on Mac runners, or skip bundling
    darwin-amd64)  echo "https://evermeet.cx/ffmpeg/getrelease/ffmpeg/zip" ;;
    darwin-arm64)  echo "" ;; # prefer system/Homebrew on Apple silicon unless you host your own
    *) echo ""; return 1 ;;
  esac
}

install_platform() {
  local plat="$1"
  local dir="$RES/$plat/bin"
  mkdir -p "$dir"
  echo "==> $plat → $dir"

  # yt-dlp
  local yu
  yu="$(ytdlp_url "$plat" || true)"
  if [ -n "$yu" ]; then
    local yname="yt-dlp"
    case "$plat" in windows-*) yname="yt-dlp.exe" ;; esac
    fetch "$yu" "$dir/$yname"
    chmod_bin "$dir/$yname"
  else
    echo "  (skip yt-dlp — no URL for $plat)"
  fi

  # ffmpeg
  local fu
  fu="$(ffmpeg_archive_url "$plat" || true)"
  if [ -z "$fu" ]; then
    echo "  (skip ffmpeg — no static build URL for $plat; use PATH at runtime)"
    return 0
  fi

  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "$tmp"' RETURN

  case "$fu" in
    *.zip)
      fetch "$fu" "$tmp/ffmpeg.zip"
      unzip -q -o "$tmp/ffmpeg.zip" -d "$tmp/out"
      ;;
    *.tar.xz)
      fetch "$fu" "$tmp/ffmpeg.tar.xz"
      mkdir -p "$tmp/out"
      tar -xJf "$tmp/ffmpeg.tar.xz" -C "$tmp/out"
      ;;
    *)
      echo "  unknown archive: $fu"
      return 1
      ;;
  esac

  # Find ffmpeg / ffprobe binaries inside extracted tree
  local ff fp
  ff="$(find "$tmp/out" -type f \( -name 'ffmpeg' -o -name 'ffmpeg.exe' \) | head -1)"
  fp="$(find "$tmp/out" -type f \( -name 'ffprobe' -o -name 'ffprobe.exe' \) | head -1)"
  if [ -z "$ff" ]; then
    echo "  ERROR: ffmpeg not found in archive"
    return 1
  fi
  local ffname="ffmpeg" fpname="ffprobe"
  case "$plat" in windows-*) ffname="ffmpeg.exe"; fpname="ffprobe.exe" ;; esac
  cp -f "$ff" "$dir/$ffname"
  chmod_bin "$dir/$ffname"
  if [ -n "$fp" ]; then
    cp -f "$fp" "$dir/$fpname"
    chmod_bin "$dir/$fpname"
  fi
  echo "  ok: $(ls -1 "$dir")"
}

host_plat() {
  local os arch
  os="$(uname -s | tr '[:upper:]' '[:lower:]')"
  arch="$(uname -m)"
  case "$os" in
    linux*) os=linux ;;
    darwin*) os=darwin ;;
    mingw*|msys*|cygwin*) os=windows ;;
  esac
  case "$arch" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
  esac
  echo "${os}-${arch}"
}

main() {
  local target="${1:-host}"
  case "$target" in
    host) install_platform "$(host_plat)" ;;
    all)
      for p in windows-amd64 windows-arm64 linux-amd64 linux-arm64 darwin-amd64; do
        install_platform "$p" || echo "WARN: $p failed"
      done
      echo "Note: darwin-arm64 ffmpeg is not auto-fetched; install via brew on Mac runners or host your own build."
      ;;
    *)
      install_platform "$target"
      ;;
  esac
}

main "$@"
