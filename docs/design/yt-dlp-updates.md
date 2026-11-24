# Signed yt-dlp updates

Status: **implemented** with the project-owned signed manifest. The update is
user-initiated from Settings → Tools and from the Home setup card.

## Why this is separate from FFmpeg

FFmpeg is pinned to a known build and checksum. yt-dlp changes frequently, and
its extractor fixes are the reason users need a newer version. The update
channel therefore uses a changing signed manifest instead of a fixed asset.
The FFmpeg installer and the yt-dlp updater share the download, checksum,
self-check, atomic-replace, backup/restore, cancellation, and managed-directory
primitives.

## Signed channel

The channel is a GitHub release named `yt-dlp-channel` with two assets:

```text
yt-dlp-manifest.json
yt-dlp-manifest.sig
```

The manifest is the exact JSON bytes that are signed. The signature is a
base64-encoded Ed25519 signature of those bytes. The app verifies the signature
before parsing the JSON, then selects the file for its platform:

```json
{
  "version": "2026.11.23",
  "generatedAt": "2026-11-24T00:00:00Z",
  "files": {
    "darwin-arm64": {
      "url": "https://github.com/yt-dlp/yt-dlp/releases/download/2026.11.23/yt-dlp_macos",
      "sha256": "…",
      "size": 37123456
    }
  }
}
```

Asset URLs must be HTTPS without credentials. The manifest covers
`windows-amd64`, `windows-arm64`, `linux-amd64`, `linux-arm64`, `darwin-amd64`,
and `darwin-arm64`; the two macOS entries point at the same universal asset.

The default source is:

```text
https://github.com/fazi-gondal/video-downloader-mygo/releases/download/yt-dlp-channel
```

Settings → Tools exposes an update-source field so a fork can use its own
channel. The embedded public key remains the trust anchor; changing the source
cannot bypass the signature.

## Public key and key custody

The app embeds this Ed25519 public key in `internal/services/ytdlpupdate.go`:

```text
05kcvsShTqh0+52Ur9v7v2uXpTC2o92OPbgH+M+MDUk=
```

The matching private seed is **not** in the repository. During this setup it was
written to:

```text
~/.config/video-downloader/ytdlp-manifest.key   (mode 0600)
```

Add it to the repository that will publish the channel:

```bash
gh secret set YTDLP_MANIFEST_PRIVATE_KEY \
  --repo 7Mrlan/video-downloader-mygo \
  < ~/.config/video-downloader/ytdlp-manifest.key
```

Anyone with this seed can sign yt-dlp updates for this app. Keep it out of the
repository and out of logs. Rotating it requires replacing the public key
constant and rebuilding the app, so treat rotation as a release decision.

## Publishing the channel

The workflow `.github/workflows/yt-dlp-channel.yml` is manual:

```bash
gh workflow run yt-dlp-channel.yml \
  --repo 7Mrlan/video-downloader-mygo \
  -f tag=2026.11.23
```

It runs `go run ./cmd/ytdlp-manifest`, which downloads the upstream assets,
records their SHA-256 and size, writes and signs the manifest, and then uploads
the two channel assets to the `yt-dlp-channel` release. Leave `tag` empty to
fetch the latest upstream release. The workflow fails clearly when
`YTDLP_MANIFEST_PRIVATE_KEY` is absent.

The upstream `fazi-gondal` channel uses the same public key only if its owner
has the matching private seed. Otherwise the upstream owner should generate a
new keypair with `go run ./cmd/ytdlp-manifest -keygen -private-key …`, replace
the embedded public key, and rebuild.

## Update flow

1. The user clicks **Update yt-dlp** in Settings → Tools or the Home setup card.
2. The app fetches the manifest and signature with a 30-second timeout and
   verifies the signature before parsing.
3. It selects the current platform, compares the manifest version with the
   effective yt-dlp version, and reports “up to date” when appropriate.
4. Otherwise it downloads the binary, verifies its SHA-256, runs
   `yt-dlp --version` and checks the reported version, backs up the old managed
   binary, and atomically replaces it.
5. Any failure or cancellation restores the old binary; success refreshes the
   probe cache and the effective path without restarting the app.

Resolution remains: explicit path, then the highest version among managed,
bundled, and system; ties prefer managed, then bundled, then system. A newer
system copy still wins, and the Tools card shows the effective source/version.

No update runs silently or on a schedule, and no auto-update is enabled.

## Risks

- **Private-key custody:** the channel is only as trustworthy as the seed. Leak
  or loss requires a new app build with a new public key.
- **Upstream assets:** the publisher trusts GitHub-hosted upstream release
  assets. The signature protects the manifest and checksums, not a compromised
  upstream release.
- **Compatibility:** `--version` is checked, but a future yt-dlp could change
  flags or post-processing behavior. A dry-run analysis of a public URL would
  be a stronger smoke test at the cost of network dependence.
- **Platform coverage:** the manifest must include every supported platform;
  a missing file fails clearly instead of falling back silently.
- **Notarization:** a downloaded yt-dlp must remain runnable after replacement;
  test this on a signed build. The helper-entitlements path is unchanged.
- **Offline/rate-limited use:** the update is optional and never blocks opening
  the app or starting a download.
- **Version comparison:** date-style yt-dlp versions are compared numerically;
  unusual nightly suffixes still need a deliberate check.

## Rollout checklist

1. Add `YTDLP_MANIFEST_PRIVATE_KEY` to the fork.
2. Run the `yt-dlp update channel` workflow for the desired tag.
3. In Settings → Tools, set the update source to
   `https://github.com/7Mrlan/video-downloader-mygo/releases/download/yt-dlp-channel`
   (or leave the upstream default when publishing there) and save.
4. Click **Update yt-dlp** and confirm the Tools card shows the new version and
   the app-managed source.
