# Releasing crmkit

This document describes how to build, version, and release the `crmkitd` binary.

## Overview

Releases are driven by the **`VERSION` file**. Bumping it on the `main` branch
triggers an automated pipeline that tags the commit and publishes multi-platform
binaries as a GitHub Release:

1. Edit `VERSION` (e.g. `0.1.0` → `0.1.1`) and merge it to `main`.
2. [`tag-release.yaml`](.github/workflows/tag-release.yaml) reads `VERSION` and,
   if the matching `v*` tag does not already exist, creates and pushes it, then
   dispatches the Release workflow.
3. [`release.yaml`](.github/workflows/release.yaml) builds the binary for each
   target platform, packages each into a `.tar.gz` (with `README.md` and
   `crmkit.example.yaml`), generates SHA-256 checksums, and creates a GitHub
   Release with auto-generated notes.

You can also release manually by pushing a tag yourself:

```bash
git tag v0.1.1
git push origin v0.1.1
```

Use [Semantic Versioning](https://semver.org/) with a `v` prefix on tags
(`v1.0.0`, not `1.0.0`). Pre-release versions: `v0.1.0-beta.1`. The `VERSION`
file itself holds the bare version (no `v` prefix); the tag adds it.

### Target platforms

| OS      | Architecture |
| ------- | ------------ |
| Linux   | amd64, arm64 |
| macOS   | amd64, arm64 |
| Windows | amd64        |

## Version embedding

Every binary embeds a version string at build time via Go linker flags. The
variable lives in [`internal/version/version.go`](internal/version/version.go)
and defaults to `"dev"` when no flag is set (e.g. when running with `go run`).
When the version is `"dev"`, update checks are skipped entirely.

In the Release workflow the version comes from the pushed tag
(`GITHUB_REF_NAME`); for local builds the Makefile derives it from
`git describe --tags`.

## Local builds

```bash
# Build ./crmkitd (version auto-detected from git tags)
make build

# Build with an explicit version
make build VERSION=v0.1.1

# Cross-compile a single platform
make cross GOOS=darwin GOARCH=arm64

# Run tests / vet
make test
make vet
```

The build is pure Go (`CGO_ENABLED=0`), so each target is a single static
binary with no runtime dependencies.

## After installing a new build

`crmkitd` never creates or alters the database schema and refuses to start until
the schema is current. After upgrading to a build that adds migrations, run the
explicit migration step once (back up first):

```bash
crmkitd migrate            # dry run: shows pending migrations, writes nothing
crmkitd migrate --execute  # apply
```

It is a no-op when the schema is already up to date. See the README for details.

## Update notifications

Release binaries check for a newer version by querying the GitHub Releases API.
The check is **skipped entirely** for `dev` builds, so it only applies to
distributed binaries.

## Versioning guidelines

- Follow [Semantic Versioning](https://semver.org/).
- Use a `v` prefix on tags (`v1.0.0`, not `1.0.0`).
- Pre-release versions: `v0.1.0-beta.1`.
- Keep the `VERSION` file in sync with the intended release tag.
