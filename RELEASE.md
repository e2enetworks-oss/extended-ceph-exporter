# Release Process

## Principles

1. **`VERSION` is the single source of truth.** `CHANGELOG.md` and the git tag
   both derive from it. `make check-version` fails the build when they disagree,
   and it runs on every pull request — not only at release time, when the fix
   would need a second tag.
2. **Semantic versioning, and the leading `v` belongs to the tag only.** The
   `VERSION` file holds `1.9.0`; the tag is `v1.9.0`. A breaking change to metric
   names, label names or configuration keys is a major bump — the metric
   namespace move from `ceph_` to `custom_` is the worked example.
3. **A release is a deliberate act.** Merging to `main` never cuts a release.
   Pushing the tag does, and only a human pushes the tag.
4. **Published version tags are immutable.** `1.9.0` is built once and never
   re-pushed. Got it wrong? Cut `1.9.1`. Only `latest`, `1.9`, `1` and `main`
   are ever allowed to move.
5. **`main` is always shippable.** Every merge publishes a container image, so
   `main` is continuously proven to build and to produce a working image.
6. **The changelog is the release notes.** The GitHub Release body is generated
   from the `CHANGELOG.md` section for that version. There is no second,
   hand-written summary to drift from it.

## Container images

Published to the GitHub Container Registry at
`ghcr.io/e2enetworks-oss/extended-ceph-exporter`.

| Tag | Moves? | Produced by | Use for |
| --- | --- | --- | --- |
| `1.9.0` | never | tag push | production; the only tag safe to pin |
| `1.9` | on each patch | tag push | tracking patch releases |
| `1` | on each minor | tag push | tracking a major line |
| `latest` | on each stable release | tag push | demos, quick starts |
| `sha-<short>` | never | merge to `main` | bisecting, pinning an unreleased fix |
| `main` | on each merge | merge to `main` | staging against unreleased `main` |

A pre-release tag (`v1.9.0-rc.1`) publishes `1.9.0-rc.1` only. It never takes
`latest`, `1.9` or `1`, and the GitHub Release is marked as a pre-release.

## Cutting a release

Say the new version is `1.9.0`.

1. Open a pull request that does both of these together:
   - set `1.9.0` in [`VERSION`](VERSION);
   - rename the `## Unreleased` heading in [`CHANGELOG.md`](CHANGELOG.md) to
     `## 1.9.0 / YYYY-MM-DD`, and make sure the entries under it read as release
     notes, because that is exactly what they become.

   Confirm locally with `make check-version` before pushing — the `ci` workflow
   runs the same check and blocks the merge otherwise.
2. Get the pull request reviewed and merged. The merge publishes `main` and
   `sha-<short>` images, but no release.
3. Tag the merge commit on `main` and push it:

   ```console
   git checkout main && git pull
   git tag -s v1.9.0 -m "v1.9.0"
   git push origin v1.9.0
   ```

4. The `release` workflow then, in order: verifies the tag matches `VERSION`,
   that the tagged commit is on `main`, and that the changelog has a section for
   it; builds and pushes the `linux/amd64` and `linux/arm64` images; extracts the
   binaries out of those exact images; and publishes the GitHub Release with the
   changelog section as its body and the binary tarballs attached.

Nothing else is manual. If the workflow fails at the verify step, no image and no
release were published — fix the tree, merge, and push a corrected tag.

## What runs when

| Event | Workflow | What it does |
| --- | --- | --- |
| Pull request opened, and every push to it | `ci` | gofmt, `go vet`, golangci-lint, `go test -race`, build, license headers, version guard |
| Pull request opened, and every push to it | `gitleaks` | scans the full git history for secrets |
| Pull request opened | `commitlint` | Conventional Commits check |
| Merge to `main` | `ci`, `gitleaks` | the same gates, on the merge commit |
| Merge to `main` | `container` | publishes `main`, `sha-<short>` |
| Push tag `v*` | `release` | verifies, publishes the version tags, cuts the GitHub Release |

## Running the gates locally

```console
make style          # gofmt
make vet            # go vet
make lint           # golangci-lint (installs it on first run)
make test           # go test -race
make check-version  # VERSION vs CHANGELOG.md
make release-notes  # preview the release body for the current VERSION
```

All of these need the Ceph development headers, because the collectors link
against librados/librbd/libcephfs through cgo. `nix develop` (see
[`flake.nix`](flake.nix)) provides them; on Debian or Ubuntu install
`libcephfs-dev librbd-dev librados-dev pkg-config`.
