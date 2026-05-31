# Contributing

Thanks for your interest in `foo`. This file covers contribution norms;
see [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) for community expectations.

## Conventional Commits

All commits and PR titles follow
[Conventional Commits v1.0.0](https://www.conventionalcommits.org/en/v1.0.0/):

```
<type>(<scope>)!?: <subject>
```

Allowed types: `feat`, `fix`, `perf`, `refactor`, `chore`, `docs`, `style`,
`test`, `ci`, `build`.

`feat`, `fix`, `perf` are user-facing (bump versions, appear in changelog).
The rest are hidden. CI/workflow changes use `ci:` — never `fix(ci):`.

Breaking changes use `!` after the type/scope or a `BREAKING CHANGE:` trailer.

## PR gate

Before opening a PR, confirm locally:

```
go build ./...
go vet ./...
go test ./...
```

CI runs the same gate plus `actionlint` on any workflow change.

## Releases

Releases are cut by [release-please](https://github.com/googleapis/release-please)
on merge to `main`. Merging the standing release PR tags `v<version>` and
triggers GoReleaser to publish binaries.
