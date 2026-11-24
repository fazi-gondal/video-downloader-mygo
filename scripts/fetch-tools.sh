#!/usr/bin/env bash
# Download yt-dlp for every platform, and static ffmpeg/ffprobe for
# Windows/Linux, into resources/<platform>/bin/
# Usage:
#   ./scripts/fetch-tools.sh                  # host platform only
#   ./scripts/fetch-tools.sh all              # every platform (for CI packaging)
#   ./scripts/fetch-tools.sh windows-amd64    # one platform
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RES="$ROOT/resources"

# Pin versions and checksums for reproducible builds (bump together when updating).
# FFmpeg-Builds publishes a checksums.sha256 file with each release; the script
# verifies against that published checksum, falling back to the pinned values
# below when the release file is unavailable.
YTDLP_TAG="${YTDLP_TAG:-2026.11.23}"
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
		windows-amd64) echo "622ed48023cac1daa52a3bf3a6ac38f03514de214b5f0fa069389fd0f6d17e5c" ;;
		windows-arm64) echo "9bb324203d02970b70dab6c9f6a48bd5ff185db7859d562cdacd1dc655ccb72a" ;;
		linux-amd64)   echo "249ddb058234276bc449b9cd9492aad9f133bda5923c61a9e39983ae114ef17d" ;;
		linux-arm64)   echo "fb847a2e4a9ddc8cf5fcfb4bb7f42ba8969ecf72068def0278074f9d300bd13c" ;;
		darwin-amd64|darwin-arm64) echo "57654854886d0398a798895aadf5173e2749fbb5e249b9f3728022ab376e11fb" ;;
		*) echo "" ;;
	esac
}

# --- ffmpeg static builds ---
# Windows/Linux: yt-dlp/FFmpeg-Builds. macOS deliberately bundles no FFmpeg:
# the app uses a Homebrew or user-supplied build there, so the installer stays
# small and a broken bundled dylib cannot shadow a working system copy.
#
# https://github.com/yt-dlp/FFmpeg-Builds/releases

ffmpeg_archive_url() {
  local plat="$1"
  local base="https://github.com/yt-dlp/FFmpeg-Builds/releases/download/${FFMPEG_RELEASE}"
  case "$plat" in
    windows-amd64) echo "$base/ffmpeg-master-latest-win64-gpl.zip" ;;
    windows-arm64) echo "$base/ffmpeg-master-latest-winarm64-gpl.zip" ;;
    linux-amd64)   echo "$base/ffmpeg-master-latest-linux64-gpl.tar.xz" ;;
    linux-arm64)   echo "$base/ffmpeg-master-latest-linuxarm64-gpl.tar.xz" ;;
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
      # Remove a stale bundle from an older checkout so local packaging cannot
      # silently ship FFmpeg after the release rule changed.
      rm -f "$dir/ffmpeg" "$dir/ffprobe"
      echo "  (skip ffmpeg — macOS uses the user's FFmpeg to keep the package small)"
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
