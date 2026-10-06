# Contributing to cracklet

Thanks for taking the time to contribute. cracklet is a small project, so the
rules are short. If something here is unclear, open an issue and ask.

## Before you start

- **Bugs and small fixes:** open a pull request directly.
- **New features or behaviour changes:** open an issue first so we can agree on
  the approach before you invest time. cracklet deliberately stays small; not
  every feature fits.
- **Security issues:** do **not** open a public issue. See [SECURITY.md](SECURITY.md).

## Development setup

You need an Apple Silicon Mac (M3 or newer) with macOS 15+ to run microVMs.
Unit tests, `go vet` and `shellcheck` also run on Linux.

```sh
git clone https://github.com/itlabs-gmbh/cracklet.git
cd cracklet
make envd     # cross-compile the guest daemon so `make build` embeds it
make build    # -> bin/cracklet
make test     # unit tests with -race
make lint     # gofmt, go vet, shellcheck
make e2e      # boots a real microVM, needs `cracklet prepare` first
```

`make lint` and `make test` must pass before you open a pull request. CI runs
the same targets, and additionally `go build ./...` without `make envd` so a
plain `go install ...@latest` keeps working: the daemon directory
`internal/envdbin/bin/` is embedded with the `all:` prefix and only holds a
`.gitkeep` in a clean checkout.

Tools: Go 1.26+, `shellcheck` (`brew install shellcheck`).

## Repository layout

| Path                | Purpose                                              |
|---------------------|------------------------------------------------------|
| `cmd/cracklet`      | CLI entry point                                      |
| `cmd/cracklet-envd` | guest identity daemon, cross-compiled for linux/arm64 |
| `internal/cli`      | cobra commands                                       |
| `internal/app`      | workflows (prepare, new, ssh, forward, ...)          |
| `internal/lima`     | `limactl` wrapper and embedded Lima template         |
| `internal/agent`    | bash agent that runs inside the Lima VM              |
| `internal/host`     | host preflight checks (`cracklet doctor`)            |
| `internal/vm`       | VM spec and port-forward validation                  |
| `internal/sshcfg`   | `~/.cracklet/ssh_config` rendering                   |
| `internal/config`   | pinned versions and SHA-256 digests                  |
| `website/`          | static project website                               |

## Coding guidelines

- **Go:** idiomatic Go, `gofmt`-formatted, `go vet` clean. Keep functions and
  files small. Return errors with context (`fmt.Errorf("...: %w", err)`),
  never swallow them.
- **Bash (`internal/agent/agent.sh`):** must pass `shellcheck -s bash`. Any
  command that changes state on the Lima VM has to hold the agent `flock` and
  roll back on failure, as the existing `new`/`rm` paths do.
- **Tests:** every change comes with tests. Table-driven tests for pure logic,
  fakes for `limactl`/`ssh` (see `internal/runner`). Pure agent helpers are
  tested under bash in `internal/agent/agent_test.go`.
- **Pinned artifacts:** bumping Firecracker, the kernel or the Ubuntu image
  means updating the SHA-256 digests in `internal/config` *and* noting it in
  the changelog, because it invalidates golden snapshots for all users.
- **Security:** cracklet's network isolation rules are documented in the
  README. A change that loosens them needs a written justification in the PR.

## Commit messages

We use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>: <short description>

<optional body explaining why, not what>
```

Types: `feat`, `fix`, `refactor`, `docs`, `test`, `chore`, `perf`, `ci`.

Examples from the history:

```
feat: show SSH command with DNS name on VM creation
fix: rebuild firewall chains atomically and block CGNAT ranges
```

## Pull requests

1. Fork and create a branch from `main`.
2. Make your change, add tests, run `make lint test`.
3. Add a line to the *Unreleased* section of [CHANGELOG.md](CHANGELOG.md).
4. Open the PR. Fill in the template: what changed, why, and how you tested
   it (`make e2e` output is welcome for anything that touches the agent or
   networking).
5. One maintainer review is required. Squash-merge is the default.

## License

By contributing you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE), the same license as the project
(Apache-2.0 §5). No separate CLA is required.
