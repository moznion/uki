# Configuration reference

uki is configured by `uki.yaml` at the repository root. `uki init` writes a starting point.

```yaml
inputs: [<glob>, ...]  # added to the inputs of every task that declares its own

tasks:            # what can be run
  <name>:
    run: <shell command>
    env:
      <KEY>: <value>
    inputs: [<glob>, ...]     # files the result depends on (enables reuse)
    artifacts: [<path>, ...]  # files stored with the record

branches:         # which tasks each branch requires (first match wins)
  - match: <glob>
    tasks: [<name>, ...]

record:           # where and how results are stored
  branch: uki/records
  remote: origin
  sign: false
  allowed_signers: <path>
  max_artifact_bytes: 10485760

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
| `artifacts` | list of strings | no | Files the task produces that are stored with the record (see [Artifacts](#artifacts)). |
| `inputs` | list of globs | no | Files the task's result depends on. A passing result recorded for the same inputs is reused instead of running the task again (see [Inputs and reuse](#inputs-and-reuse)). Without it the task always runs. |

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

### Artifacts

`artifacts` lists files a task produces, such as a coverage profile or a test report, that should be available after the run, for example to a GitHub Actions job that comments coverage on a pull request. `uki run` stores them on the records branch next to the record, and `uki artifact get` reads them back.

```yaml
tasks:
  test:
    run: go test -coverprofile=coverage.out ./...
    artifacts: [coverage.out]
```

- Paths are relative to the repository root, slash-separated and clean (`build/report.xml`, not `./build/report.xml` or `../x`).
- Artifacts must be ignored by git (`.gitignore`), not tracked: a tracked file is already in the commit. `uki run` refuses to start when an artifact of a task it is about to run is tracked.
- A task with artifacts needs a name that works as a directory name: no `/` or `\`, and not `.` or `..`.
- Each file is copied as soon as its task ends, so a later task cannot change what is stored. Artifacts are stored whether the task passed or failed.
- A file that is missing, is not a regular file (a symlink, a directory), resolves outside the repository, or is larger than [`record.max_artifact_bytes`](#record) is left out with a warning. The task result is not affected.
- Running a task again for the same commit replaces all of its artifacts, including dropping files the new run did not produce.
- Nothing is stored with `--no-record`, or when the run is not recorded for any other reason.

Artifacts are published to everyone who can read the repository, and they stay in the history of the records branch. Do not list files that may contain secrets.

### Inputs and reuse

A task that declares `inputs` is not run again when nothing it depends on has changed: `uki run` reuses a passing result recorded for another commit with the same inputs, and records it for `HEAD` as reused.

```yaml
inputs: [go.mod, go.sum, .tool-versions]  # shared by every task with inputs

tasks:
  backend-test:
    run: go test ./backend/...
    inputs: ["backend/**", "schema/**"]
  frontend-test:
    run: npm test --prefix frontend
    inputs: ["frontend/**"]
  vuln:
    run: govulncheck ./...   # no inputs: always runs
```

How it works:

- Patterns use the [glob syntax](#glob-syntax) of branch rules (`*` and `?` stay within one directory, `**` crosses them), matched against repository-relative paths. There is no negation.
- The top-level `inputs` are added to every task that declares its own. Tasks without `inputs` ignore them and always run.
- The inputs key of a task at a commit is the SHA-256 of: the commit's `uki.yaml`, the task name, and the mode, object id and path of every committed file the patterns match (submodules count by their commit). It is computed from the commit with `git ls-tree`; the working tree is not read. Any change to `uki.yaml`, even a comment, therefore re-runs every task.
- After a real, passing run, the key is indexed on the records branch at `inputs/<first two hex chars>/<key>.json`, pointing at the commit. Failures and reused results are not indexed.
- `uki run` looks the key up before running each task. Any commit can be the source: an ancestor, a commit you rebased away from, or one recorded by someone else. A reused result keeps the original command, outcome, log digest and [artifacts](#artifacts), and adds `reused_from` (always the commit the task really ran on).
- Re-running the same commit runs the task again. `uki run --no-reuse` runs everything (its results are still indexed), and `--no-record` never reuses.

On GitHub, `uki mark` checks every reused result with the default branch's `uki.yaml`: the task must declare `inputs` there, the commit it came from must have a real passing run, and the files matched by the default branch's patterns, as well as `uki.yaml` itself, must be identical in both commits. Otherwise the status is `error`, so a work branch cannot narrow its own inputs to skip tasks.

Declare everything the result depends on. A missing input means a stale pass: if `backend-test` also reads `shared/`, but `shared/**` is not listed, a change there is never tested until something listed changes. Leave out `inputs` for tasks whose result depends on things outside the repository, such as vulnerability databases, base images or package registries, so they always run.

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
| `max_artifact_bytes` | integer | `10485760` (10 MiB) | Largest size of one [artifact](#artifacts) file. Larger files are left out of the record with a warning. GitHub rejects pushes containing files over 100 MB. |

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
    run: go test -race -coverprofile=coverage.out ./...
    artifacts: [coverage.out]
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
  max_artifact_bytes: 10485760

status:
  context: uki
```

More complete setups are in [`examples/`](../examples/).
