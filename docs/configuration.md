# Configuration reference

uki is configured by `uki.yaml` at the repository root. `uki init` writes a starting point.

```yaml
tasks:            # what can be run
  <name>:
    run: <shell command>
    env:
      <KEY>: <value>

branches:         # which tasks each branch requires (first match wins)
  - match: <glob>
    tasks: [<name>, ...]

record:           # where and how results are stored
  branch: uki/records
  remote: origin
  sign: false
  allowed_signers: <path>

status:           # how results appear on GitHub
  context: uki
```

Unknown keys are rejected, at any level: a typo such as `taks:` or `evn:` makes every command fail with a parse error instead of being silently ignored. An empty file is valid and uses all defaults (but defines no tasks).

## Which copy of `uki.yaml` is used

| Command | Reads `uki.yaml` from |
|---|---|
| `uki run` | the working tree (it must be committed: a dirty tree is never recorded) |
| `uki status` | the working tree; warns when it differs from the remote default branch |
| `uki mark` (GitHub Actions) | the **default branch** checkout |

Because `uki mark` uses the default branch's rules, changes to `branches`, `record.allowed_signers` or `status.context` take effect only after they are merged into the default branch. A work branch cannot relax its own requirements.

In addition, every task result stores the SHA-256 of `uki.yaml` at the recorded commit, and `uki mark` reports `error` if it does not match the commit's `uki.yaml`.

## `tasks`

A map from task name to task definition. Task names are free-form strings and are what appears in records, `uki status` output and status descriptions.

| Key | Type | Required | Description |
|---|---|---|---|
| `run` | string | yes | Command executed with `sh -c`, from the repository root. |
| `env` | map of string to string | no | Extra environment variables. They are added on top of the environment `uki` was started with. |

A task passes when its command exits with status `0`.

```yaml
tasks:
  lint:
    run: golangci-lint run ./...
  test:
    run: go test -race ./...
    env:
      CGO_ENABLED: "1"
  e2e:
    run: |
      docker compose up -d
      trap 'docker compose down' EXIT
      go test -tags=e2e ./e2e/...
```

Notes:

- YAML values that look like numbers or booleans must be quoted in `env` (`"1"`, `"true"`).
- Tasks must not leave the working tree dirty. If a task generates files, list them in `.gitignore`; otherwise the result is not recorded.
- Tasks must not create commits or switch branches. If `HEAD` moves during a run, the result is not recorded.
- The task's output is streamed to the terminal and only its SHA-256 is recorded.

## `branches`

An ordered list of rules. For a given branch, the **first** rule whose `match` matches decides which tasks are required:

- `uki run` with no task names runs exactly these tasks.
- `uki mark` and `uki status` require each of these tasks to have a passing result.

| Key | Type | Required | Description |
|---|---|---|---|
| `match` | string | yes | Glob matched against the full branch name (e.g. `feature/login`, not `refs/heads/feature/login`). |
| `tasks` | list of task names | no | Tasks required on matching branches. Every name must be defined in `tasks`. An empty list requires nothing: any record for the commit yields `success`. |

### Glob syntax

| Pattern | Matches |
|---|---|
| `*` | Any sequence of characters except `/` |
| `?` | Any single character except `/` |
| `**` | Any sequence of characters, including `/` |
| anything else | Itself, literally |

| `match` | `main` | `feature/a` | `feature/a/b` | `release-1` | `release-10` |
|---|---|---|---|---|---|
| `main` | ✓ | | | | |
| `feature/*` | | ✓ | | | |
| `feature/**` | | ✓ | ✓ | | |
| `release-?` | | | | ✓ | |
| `release-*` | | | | ✓ | ✓ |
| `**` | ✓ | ✓ | ✓ | ✓ | ✓ |

### Branches without a matching rule

- `uki run` without task names fails with `no branch rule matches "<branch>"`. Explicit task names still work (`uki run lint`).
- `uki mark` and `uki status` report **`pending`** ("No branch rule in uki.yaml matches this branch"), whatever has been recorded. A required `uki` status check therefore blocks merging such a branch until a rule covers it.

To cover every branch, end the list with a catch-all rule:

```yaml
branches:
  - match: main
    tasks: [lint, test, e2e]
  - match: "**"
    tasks: [lint, test]
```

Quote patterns that start with `*` (`"**"`, `"*-wip"`): an unquoted leading `*` is a YAML alias.

## `record`

| Key | Type | Default | Description |
|---|---|---|---|
| `branch` | string | `uki/records` | Orphan branch that stores the records. Created by the first `uki run`. |
| `remote` | string | `origin` | Git remote the records branch is fetched from and pushed to. |
| `sign` | bool | `false` | Create record commits with `git commit -S`, using your git signing configuration. |
| `allowed_signers` | string | *(unset)* | Repository-relative path to an SSH allowed signers file. When set, `uki mark` and `uki status` require a valid SSH signature from a listed key. |

### Changing `record.branch`

Nothing else needs to change. The generated workflow runs `uki mark --github-event`, which reads `record.branch` from `uki.yaml` on the default branch to tell a push to the records branch from a push to a work branch. Records already stored under the old name are not moved; re-run `uki run` for commits that still need a status.

### `sign` and `allowed_signers`

`sign` only controls how *your* `uki run` creates commits. `allowed_signers` controls what `uki mark` *accepts*. Enabling `allowed_signers` without everyone signing makes every status `error`, so roll out in this order:

1. Every developer configures SSH commit signing and sets `sign: true` (it can also be enabled with `commit.gpgsign=true` in their git config).
2. Commit the allowed signers file to the default branch.
3. Set `allowed_signers` on the default branch.

The file uses the format of `ssh-keygen`'s ALLOWED SIGNERS section, one key per line, with the signer's email (as used for their commits) as the principal:

```
alice@example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI...
```

What is verified: the most recent commit on the records branch that modified the record of the commit being judged. The commit must carry an SSH signature (GPG signatures are rejected) that `git` reports as good (`%G?` = `G`) against the allowed signers file. The file is read from the default branch checkout.

## `status`

| Key | Type | Default | Description |
|---|---|---|---|
| `context` | string | `uki` | Name of the commit status on GitHub. This is the name to require in branch protection. |

## Full example

```yaml
tasks:
  fmt:
    run: test -z "$(gofmt -l .)"
  lint:
    run: golangci-lint run ./...
  test:
    run: go test -race ./...
  e2e:
    run: go test -tags=e2e ./e2e/...
    env:
      E2E_TIMEOUT: "10m"

branches:
  - match: main
    tasks: [fmt, lint, test, e2e]
  - match: "release/**"
    tasks: [fmt, lint, test, e2e]
  - match: "docs/**"
    tasks: [fmt]
  - match: "**"
    tasks: [fmt, lint, test]

record:
  branch: uki/records
  remote: origin
  sign: true
  allowed_signers: .uki/allowed_signers

status:
  context: uki
```

More complete setups are in [`examples/`](../examples/).
