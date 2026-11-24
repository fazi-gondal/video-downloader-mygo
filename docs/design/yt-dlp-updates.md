# Proposal: in-app yt-dlp updates

Status: **proposal only; not implemented.** The setup card currently installs
FFmpeg/FFprobe and lets the user choose an existing yt-dlp path. It does not
download or update yt-dlp.

## Why this is separate from FFmpeg

FFmpeg is pinned to a known build and checksum. yt-dlp changes frequently, and
its extractor fixes are the reason users need a newer version. An in-app
updater needs a trusted, changing source and a compatibility check, so it
carries a different risk profile from the fixed FFmpeg download.

## Reuse the FFmpeg installer

The update can reuse the same primitives:

- download with progress and cancellation
- SHA-256 verification before execution
- `yt-dlp --version` self-check
- atomic replace from a temporary directory
- backup/restore on any failure
- managed directory under the app's Application Support folder
- probe-cache reset and immediate service refresh

The entry belongs in the setup card and Settings → Tools, next to FFmpeg. It
must be user-initiated; no silent or scheduled update.

## Trust model

Two options:

1. **GitHub release + `SHA2-256SUMS` (simple).** Query the latest release,
   download the platform asset, and verify against the checksum published in
   the same release. This protects against transport corruption, but not a
   compromised or rewritten release: the checksum and binary share a trust
   root.
2. **Project-owned signed manifest (recommended).** CI downloads the upstream
   asset, verifies it, and publishes a manifest signed with an Ed25519 key whose
   public half is embedded in the app. The app verifies the manifest and the
   asset before installing. This adds release automation and key management,
   but gives a real update trust root and supports rollback metadata.

A third option is to keep relying on the user's system copy, which the app
already prefers when it is newer. That requires no new trust root.

## Resolution and rollback

- Store the managed binary in the same app-owned directory as FFmpeg.
- Resolve yt-dlp as: explicit path, then the highest version among managed,
  bundled, and system; ties prefer managed, then bundled, then system.
- Keep the previous binary until the new one passes `--version` and is moved
  into place. On failure, restore it.
- Show the installed and effective versions in Settings → Tools. If a newer
  system copy exists, keep using it and say so.

## Risks

- **Supply chain:** a latest-release checksum is not an independent signature;
  a signed project manifest is stronger but requires a private key.
- **Compatibility:** a future yt-dlp can change flags or post-processing
  behavior. `--version` is not enough; a dry-run analysis of a known public URL
  would be a better smoke test, but adds network dependence.
- **Platform coverage:** macOS uses a universal build; Windows and Linux need
  separate assets. The update must fail clearly when no asset exists.
- **Notarization:** the bundled binary is signed with the app's helper
  entitlements. A downloaded yt-dlp must remain runnable after replacement;
  this needs testing on a signed build.
- **Partial downloads and cancellation:** reuse the tested temporary-directory
  and rollback path, not a direct overwrite.
- **Rate limits and offline use:** updates must be optional and never block
  opening the app or starting a download.
- **Version comparison:** use yt-dlp's date version; do not compare arbitrary
  strings.

## Recommendation

Do not add the updater until the trust model is chosen. The existing explicit
path and newer-system-copy preference already solve the immediate "use a fixed
extractor without reinstalling the app" need. If an in-app updater is wanted,
prefer the signed manifest path and implement it as a separate change with its
own tests and a staged rollout.
