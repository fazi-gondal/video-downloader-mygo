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
# Static macOS FFmpeg builds: self-contained, unlike Homebrew bottles whose
# binaries link /opt/homebrew dylibs that are absent on users' machines.
FFMPEG_STATIC_TAG="${FFMPEG_STATIC_TAG:-b6.1.1}"

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
  local base="https://github.com/yt-dlp/yt-dlp/releases/download/${YTDLP_TAG}"
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
# Windows/Linux: yt-dlp/FFmpeg-Builds
# macOS: Homebrew's verified ffmpeg bottle on a runner matching the target
# architecture. The yt-dlp build repository does not publish macOS archives.
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
    darwin-amd64|darwin-arm64) echo "" ;; # installed from a verified Homebrew bottle
    *) echo ""; return 1 ;;
  esac
}

ffmpeg_sha256() {
	local plat="$1"
	case "$plat" in
        windows-amd64) echo "a20f67d27f375cde0a5dc763a83d5b80448a934ef814556ea3f6e353b70c4036" ;;
        windows-arm64) echo "83959adb9c98692c5988d5a953bb5a35c60546a9340e86ee13b61b1b225b586f" ;;
        linux-amd64)   echo "bce7caa8cb6b82a552f207689f79c3cf9da0bd12db3cd539f0dc3e18213f0417" ;;
        linux-arm64)   echo "2b9a9cdec839f7b546f27c686c3f1c31dbc4cda2493fbc4caf173e06c8ca77b2" ;;
        *) echo "" ;;
    esac
}

get_ffmpeg_expected_sha256() {
  local plat="$1" archive_name="$2"
  if [ -n "${FFMPEG_SHA256:-}" ]; then
    echo "$FFMPEG_SHA256"
    return
  fi
  local cs_url="https://github.com/yt-dlp/FFmpeg-Builds/releases/download/${FFMPEG_RELEASE}/checksums.sha256"
  local remote_hash
  remote_hash="$(curl -fsSL "$cs_url" 2>/dev/null | grep "${archive_name}" | cut -d' ' -f1 || true)"
  if [ -n "$remote_hash" ]; then
    echo "$remote_hash"
    return
  fi
  ffmpeg_sha256 "$plat"
}

# ffmpeg_static_sha256 pins the eugeneware/ffmpeg-static asset for a target.
ffmpeg_static_sha256() {
  local plat="$1" tool="$2"
  case "$plat:$tool" in
    darwin-arm64:ffmpeg)  echo "a90e3db6a3fd35f6074b013f948b1aa45b31c6375489d39e572bea3f18336584" ;;
    darwin-arm64:ffprobe) echo "bb2db6f5d8cef919da12fbf592119a987202a8c060a886f3cab091f9cab90b64" ;;
    darwin-amd64:ffmpeg)  echo "ebdddc936f61e14049a2d4b549a412b8a40deeff6540e58a9f2a2da9e6b18894" ;;
    darwin-amd64:ffprobe) echo "fa3add0ce901f7241abe0dfc0155d958fc834aca3f8ce61f87cc712ae669c1e0" ;;
  esac
}

install_macos_ffmpeg() {
  local plat="$1"
  local dir="$RES/$plat/bin"
  local arch="arm64"
  case "$plat" in
    darwin-amd64) arch="x64" ;;
  esac

  local tool name url tmp
  for tool in ffmpeg ffprobe; do
    name="${tool}-darwin-${arch}"
    url="https://github.com/eugeneware/ffmpeg-static/releases/download/${FFMPEG_STATIC_TAG}/${name}"
    tmp="$dir/.${tool}.download"
    fetch "$url" "$tmp"
    verify_sha256 "$tmp" "$(ffmpeg_static_sha256 "$plat" "$tool")"
    mv -f "$tmp" "$dir/$tool"
    chmod +x "$dir/$tool"
  done

  # A Homebrew-linked binary would run on the build machine but fail on a
  # user's Mac, which is the bug these static builds replace.
  if command -v otool >/dev/null 2>&1; then
    if otool -L "$dir/ffmpeg" | grep -Eq '(/opt/homebrew|/usr/local/opt)/'; then
      echo "  ERROR: bundled FFmpeg links Homebrew libraries" >&2
      return 1
    fi
  fi

  echo "  ok: static FFmpeg $("$dir/ffmpeg" -version | awk 'NR==1 {print $3}')"
  echo "  sha256: $(sha256 "$dir/ffmpeg")  ffmpeg"
  echo "  sha256: $(sha256 "$dir/ffprobe")  ffprobe"
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
  case "$plat" in
    darwin-amd64|darwin-arm64)
      install_macos_ffmpeg "$plat"
      echo "  ok: $(ls -1 "$dir")"
      return 0
      ;;
  esac

  local fu
  fu="$(ffmpeg_archive_url "$plat" || true)"
  if [ -z "$fu" ]; then
    echo "  (skip ffmpeg — no static build URL for $plat; use PATH at runtime)"
    return 0
  fi

  local tmp
  tmp="$(mktemp -d)"
  trap 'rm -rf "${tmp:-}"' RETURN

	case "$fu" in
		*.zip)
			fetch "$fu" "$tmp/ffmpeg.zip"
			verify_sha256 "$tmp/ffmpeg.zip" "$(get_ffmpeg_expected_sha256 "$plat" "$(basename "$fu")")"
			unzip -q -o "$tmp/ffmpeg.zip" -d "$tmp/out"
			;;
		*.tar.xz)
			fetch "$fu" "$tmp/ffmpeg.tar.xz"
			verify_sha256 "$tmp/ffmpeg.tar.xz" "$(get_ffmpeg_expected_sha256 "$plat" "$(basename "$fu")")"
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
  rm -rf "$tmp"
  trap - RETURN
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
      for p in windows-amd64 windows-arm64 linux-amd64 linux-arm64 darwin-amd64 darwin-arm64; do
        install_platform "$p"
      done
      ;;
    *)
      install_platform "$target"
      ;;
  esac
}

main "$@"
