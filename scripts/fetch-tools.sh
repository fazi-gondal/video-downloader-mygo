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
  chmod +x "$f"
}

# --- yt-dlp (single-file binaries from GitHub releases) ---
# https://github.com/yt-dlp/yt-dlp/releases
ytdlp_asset_name() {
  local plat="$1"
  case "$plat" in
    windows-amd64) echo "yt-dlp.exe" ;;
    windows-arm64) echo "yt-dlp_arm64.exe" ;;
    linux-amd64)   echo "yt-dlp_linux" ;;
    linux-arm64)   echo "yt-dlp_linux_aarch64" ;;
    darwin-amd64)  echo "yt-dlp_macos" ;;
    darwin-arm64)  echo "yt-dlp_macos" ;; # universal-ish; arm works with macos build
    *) echo ""; return 1 ;;
  esac
}

download_ytdlp() {
  local plat="$1" dest="$2"
  local asset
  asset="$(ytdlp_asset_name "$plat" || true)"
  if [ -z "$asset" ]; then
    echo "  (skip yt-dlp — no asset defined for $plat)"
    return 0
  fi

  local preferred_tag="${YTDLP_TAG:-2026.08.19}"
  local tmp="${dest}.download"

  download_asset() {
    local tag="$1"
    local url="https://github.com/yt-dlp/yt-dlp/releases/download/${tag}/${asset}"
    echo "  Attempting to download ${url} ..."
    curl -fL --retry 3 --retry-delay 5 -o "$tmp" "$url"
  }

  echo "  Downloading yt-dlp (preferred tag: ${preferred_tag})"
  if download_asset "$preferred_tag"; then
    echo "  Downloaded yt-dlp from tag ${preferred_tag}"
    local expected_hash="${YTDLP_SHA256:-$(ytdlp_sha256 "$plat")}"
    if [ -n "$expected_hash" ]; then
      verify_sha256 "$tmp" "$expected_hash"
    fi
  else
    echo "  Failed to download yt-dlp for tag ${preferred_tag}; trying latest release..."
    local latest_tag=""
    latest_tag="$(curl -sS https://api.github.com/repos/yt-dlp/yt-dlp/releases/latest \
                  | grep -m1 '"tag_name":' \
                  | sed -E 's/.*"([^"]+)".*/\1/' || true)"

    if [ -z "$latest_tag" ]; then
      echo "  ERROR: Could not determine latest tag from GitHub API" >&2
      return 1
    fi

    echo "  Latest tag is ${latest_tag}; attempting download..."
    if download_asset "$latest_tag"; then
      echo "  Downloaded yt-dlp from latest release (${latest_tag})"
      echo "  (Skipping hardcoded SHA-256 check because tag ${latest_tag} != pinned ${preferred_tag})"
    else
      echo "  ERROR: Failed to download yt-dlp from latest release ${latest_tag}" >&2
      return 1
    fi
  fi

  mv -f "$tmp" "$dest"
  chmod_bin "$dest"
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
        windows-amd64) echo "dfb89d02948f94b77a89c218e6abbb16032c19a329da972886f9029b82b229e0" ;;
        windows-arm64) echo "4ad7d4e3e842cd6eeeee9ffac696e34c0b0b6768c209190c931027ec81325b8e" ;;
        linux-amd64)   echo "9b1136f703bb207d7653a6504ae8dce135fec41ec9fb61eb9117d2fc2dd98d16" ;;
        linux-arm64)   echo "298b31e41cc11627ddcccd815dc74dc04c99ebb3a45916e23bbda5ea80699a83" ;;
        *) echo "" ;;
    esac
}

install_macos_ffmpeg() {
  local plat="$1"
  local dir="$RES/$plat/bin"
  if ! command -v brew >/dev/null 2>&1; then
    echo "  ERROR: brew is required to bundle FFmpeg for $plat" >&2
    return 1
  fi

  # Homebrew verifies the bottle checksum before installing it. The runner
  # must match the target architecture (macos-13 for amd64, macos-latest for
  # arm64); otherwise an incompatible executable could be packaged silently.
  brew install --formula ffmpeg
  local prefix
  prefix="$(brew --prefix ffmpeg)"
  [ -x "$prefix/bin/ffmpeg" ] || { echo "  ERROR: brew ffmpeg missing" >&2; return 1; }
  [ -x "$prefix/bin/ffprobe" ] || { echo "  ERROR: brew ffprobe missing" >&2; return 1; }
  cp -f "$prefix/bin/ffmpeg" "$dir/ffmpeg"
  cp -f "$prefix/bin/ffprobe" "$dir/ffprobe"
  chmod +x "$dir/ffmpeg" "$dir/ffprobe"
  echo "  ok: Homebrew FFmpeg $("$dir/ffmpeg" -version | awk 'NR==1 {print $3}')"
  echo "  sha256: $(sha256 "$dir/ffmpeg")  ffmpeg"
  echo "  sha256: $(sha256 "$dir/ffprobe")  ffprobe"
}

install_platform() {
  local plat="$1"
  local dir="$RES/$plat/bin"
  mkdir -p "$dir"
  echo "==> $plat → $dir"

  # yt-dlp
  local yname="yt-dlp"
  case "$plat" in windows-*) yname="yt-dlp.exe" ;; esac
  download_ytdlp "$plat" "$dir/$yname"

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
  rm -rf "${tmp:-}"
  trap - RETURN
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
