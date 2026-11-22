# Releasing assay

Releases are built on your machine with [GoReleaser](https://goreleaser.com) (`brew install goreleaser`). One command builds macOS and Linux binaries (arm64 and amd64), publishes them as a GitHub Release with checksums, and updates the Homebrew cask in [jluna4072/homebrew-tap](https://github.com/jluna4072/homebrew-tap), so `brew install jluna4072/tap/assay` and `brew upgrade assay` pick it up.

## Once

- The assay repository must be public, so Homebrew can download release archives.
- Create the tap repository, public and named `homebrew-tap` (Homebrew requires the `homebrew-` prefix): `gh repo create jluna4072/homebrew-tap --public`.

## Each release

From an up-to-date `main` with a clean working tree, and the tests passing:

```sh
git tag -a v0.1.0 -m "assay v0.1.0"
git push origin v0.1.0
GITHUB_TOKEN=$(gh auth token) HOMEBREW_TAP_TOKEN=$(gh auth token) goreleaser release --clean
```

The version comes from the tag; `assay --version` reports it. Then check it installs:

```sh
brew update && brew install jluna4072/tap/assay && assay doctor
```

To try a release without publishing anything: `HOMEBREW_TAP_TOKEN=x goreleaser release --snapshot --clean --skip=publish`. Archives and the cask are written to `dist/`, which is ignored.

## Signing

The binaries aren't signed or notarized. The cask clears macOS's quarantine flag after install, which Homebrew sets on cask downloads, so the binary runs without a Gatekeeper prompt. Direct downloads from the releases page keep the flag; run `xattr -d com.apple.quarantine assay` once. Signing with an Apple Developer ID can be added later if direct downloads matter.
