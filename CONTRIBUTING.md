# Contributing

## Commit messages

This repo uses [Conventional Commits](https://www.conventionalcommits.org/).
Releases are cut **automatically** from commit messages on merge to `main` — CI
delegates to `strongo/go-ci-action`, which reads the prefix and tags the release
itself. There is no human step between your commit message and a published version.

```
feat(whatsapp): add interactive list degradation
fix(whatsapp): stop statuses advancing the customer service window
docs(whatsapp): document the degradation ladder
chore(deps): bump bots-api-whatsapp to v0.2.0
```

### ⛔ Never mark a commit as a breaking change

- ❌ No `!` suffix — `feat(whatsapp)!:`
- ❌ No `BREAKING CHANGE:` footer

This module is **pre-release (`v0.x`)**. Go's semver rules make no compatibility
promise below v1, so a minor bump is already allowed to break callers — the marker
communicates nothing actionable, and it causes two real problems:

1. **It silently breaks the bump.** The `!` stops the type being recognised, so the
   commit falls through to a default **patch**. Measured: `feat(whatsapp)!:` shipped
   as `v0.1.1` instead of `v0.2.0`. A marked feature disappears from anyone tracking
   minors.
2. **It risks an irreversible `v1.0.0`.** The current parser ignores `!`, but that is
   a property of one action version. If an upgrade starts honouring it — or honours
   a `BREAKING CHANGE:` footer, which is recognised more widely — a routine `fix!`
   cuts v1.0.0. The Go module proxy caches tags immutably, so that cannot be recalled.

**Removing or changing API is fine and expected pre-release.** Just describe it as
prose in the commit body:

```
feat(whatsapp): progressively degrade rich messages instead of rejecting them

Removed ErrEditNotSupported, ErrFormatNotSupported and ErrKeyboardNotSupported.
Nothing consumed them; the repo is days old.
```

Revisit only if this module deliberately reaches v1.0.0.

## Before pushing

```shell
gofmt -l .        # must be empty
go vet ./...
golangci-lint run # must be 0 issues
go test ./...     # must pass, and must not skip
```

Skipped tests are treated as failures here. A sibling client in this org has a test
suite that is almost entirely `t.Skip()`, which reads as green while testing nothing.

Enable the local hooks with `git config core.hooksPath .git-hooks`.

## Verifying against Meta's docs

Do not write a wire format from memory. Two bugs in this codebase came from exactly
that — a `Retry-After` header that does not exist, and a Graph API version four
releases stale.

Meta's docs serve **HTTP 500 to plain fetchers** and 400 to `curl`; only a real
browser gets 200. The reliable path is appending **`.md`** to any
`/documentation/business-messaging/whatsapp/...` URL, which returns the page as
markdown. (It 404s on the legacy `/docs/whatsapp/...` paths, which now redirect.)

If you cannot verify a shape, do not guess it — leave it unimplemented and say so in
the [roadmap](README.md#roadmap).
