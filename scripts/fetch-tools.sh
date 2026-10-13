#!/usr/bin/env bash
# Download yt-dlp + static ffmpeg/ffprobe into resources/<platform>/bin/
# Usage:
#   ./scripts/fetch-tools.sh                  # host platform only
#   ./scripts/fetch-tools.sh all              # every platform (for CI packaging)
#   ./scripts/fetch-tools.sh windows-amd64    # one platform
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RES="$ROOT/resources"

# Pin versions and checksums for reproducible builds (bump together when updating).
# FFmpeg-Builds publishes its rolling build under the immutable-by-content
# checksum below; a changed rolling asset fails verification until its checksum
# is deliberately updated here.
YTDLP_TAG="${YTDLP_TAG:-2026.08.19}"
FFMPEG_RELEASE="${FFMPEG_RELEASE:-latest}"

fetch() {
	local url="$1" out="$2"
	echo "  ← $url"
	curl -fsSL --retry 3 -o "$out" "$url"
}

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
		return
	fi
	shasum -a 256 "$1" | awk '{print $1}'
}

verify_sha256() {
	local file="$1" expected="$2" actual
	if [ -z "$expected" ]; then
		echo "  ERROR: no SHA-256 checksum configured for $file" >&2
		return 1
	fi
	actual="$(sha256 "$file")"
	if [ "$actual" != "$expected" ]; then
		echo "  ERROR: SHA-256 mismatch for $file" >&2
		echo "    expected: $expected" >&2
		echo "    actual:   $actual" >&2
		return 1
	fi
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
    windows-arm64) echo "$base/yt-dlp_arm64.exe" ;;
    linux-amd64)   echo "$base/yt-dlp_linux" ;;
    linux-arm64)   echo "$base/yt-dlp_linux_aarch64" ;;
    darwin-amd64)  echo "$base/yt-dlp_macos" ;;
    darwin-arm64)  echo "$base/yt-dlp_macos" ;; # universal-ish; arm works with macos build
    *) echo ""; return 1 ;;
  esac
}

ytdlp_sha256() {
	local plat="$1"
	case "$plat" in
		windows-amd64) echo "66674953fe251b89f4d08c5f0e35e0728679bd67ab3d7d05c0562af101dd3e7a" ;;
		windows-arm64) echo "05b438997bafc3affdfda9d041353c9d73e04dc842207254b655b0887c4445b0" ;;
		linux-amd64)   echo "58162f9bfdc27458ea47bfcb311cf47028f17d8154a8bf7d689861d46399230a" ;;
		linux-arm64)   echo "b16e4dab368a816cd05d477d698a605a6ae87ccee1c8ffd38fa21d7254141fcc" ;;
		darwin-amd64|darwin-arm64) echo "0f192b7ec147ab6288885d6351d9ab67367640029b4377576ef46dd79cf7b202" ;;
		*) echo "" ;;
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
  local base="https://github.com/yt-dlp/FFmpeg-Builds/releases/download/${FFMPEG_RELEASE}"
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

ffmpeg_sha256() {
	local plat="$1"
	case "$plat" in
		windows-amd64) echo "549f730b6ca62f5c7d099241e37e6aaa8f66fc2fecb1dc3219802788e0ff4e43" ;;
		windows-arm64) echo "71eaa76f74762f79b5b7e7a7680fc0572b42b8c5f103ffb34fce10e23820eb68" ;;
		linux-amd64)   echo "d80181ebe334add8c17b17d73e5291241e6ea243dd8eaf524d58be1b6e34f770" ;;
		linux-arm64)   echo "2a17294e25d01f3742eaed9d1ae3658e165341894d265793189cd7eccef6573c" ;;
		*) echo "" ;;
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
		local ytmp="$dir/.${yname}.download"
		fetch "$yu" "$ytmp"
		verify_sha256 "$ytmp" "${YTDLP_SHA256:-$(ytdlp_sha256 "$plat")}"
		mv -f "$ytmp" "$dir/$yname"
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
			verify_sha256 "$tmp/ffmpeg.zip" "${FFMPEG_SHA256:-$(ffmpeg_sha256 "$plat")}"
			unzip -q -o "$tmp/ffmpeg.zip" -d "$tmp/out"
			;;
		*.tar.xz)
			fetch "$fu" "$tmp/ffmpeg.tar.xz"
			verify_sha256 "$tmp/ffmpeg.tar.xz" "${FFMPEG_SHA256:-$(ffmpeg_sha256 "$plat")}"
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
	ff="$(find "$tmp/out" -type f \( -name 'ffmpeg' -o -name 'ffmpeg.exe' \) -print -quit)"
	fp="$(find "$tmp/out" -type f \( -name 'ffprobe' -o -name 'ffprobe.exe' \) -print -quit)"
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
        install_platform "$p"
      done
      echo "Note: darwin-arm64 ffmpeg is not auto-fetched; install via brew on Mac runners or host your own build."
      ;;
    *)
      install_platform "$target"
      ;;
  esac
}

main "$@"
