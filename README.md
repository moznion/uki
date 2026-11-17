# uki

**Run CI on your own machine, publish only the verdict to GitHub.**

uki is a task runner that records the result of your checks (tests, linters, builds, ...) for a specific commit, and a small GitHub Actions job that turns those records into [commit statuses](https://docs.github.com/en/rest/commits/statuses). Heavy work happens on developers' machines; GitHub only receives a lightweight, verifiable "this commit passed / failed".

```
 developer machine                           GitHub
 ─────────────────                           ──────
 uki run
  ├─ refuse to record if the tree is dirty
  ├─ run the tasks required for the branch
  └─ push records/<sha[:2]>/<sha>.json ───▶  uki/records branch ──┐
                                                                  │ push
 git push ─────────────────────────────────▶ feature branch ──────┤ triggers
                                                                  ▼
                                             GitHub Actions: uki mark
                                              ├─ read rules from the default branch
                                              ├─ verify record (commit, tree, config, signature)
                                              └─ set commit status "uki"
                                                 success / failure / pending / error
```

Pushing the code and pushing the record can happen in either order: both pushes trigger the workflow, and whichever runs last sets the final status.

## Contents

- [Installation](#installation)
- [Quick start](#quick-start)
- [Commands](#commands)
- [How a commit is judged](#how-a-commit-is-judged)
- [Records](#records)
- [Signing records](#signing-records)
- [Security model](#security-model)
- [Configuration reference](docs/configuration.md)
- [Examples](examples/)

## Installation

```sh
go install github.com/moznion/uki/cmd/uki@latest
```

Requirements: `git` (2.42 or later) and `sh`. `ssh-keygen` is needed only when record signatures are verified.

## Quick start

1. Generate the config and the workflow in your repository:

   ```sh
   uki init
   ```

   This writes `uki.yaml` and `.github/workflows/uki.yml`. Edit `uki.yaml` to describe your tasks (see the [configuration reference](docs/configuration.md) and the [examples](examples/)).

2. Commit both files and push them to the default branch.

3. On any branch, with a clean working tree and the commit pushed (or about to be):

   ```sh
   uki run
   ```

   The tasks required for the current branch run; the result is committed to the `uki/records` branch and pushed. The first run creates that branch.

4. In the repository settings, add a branch protection rule (or ruleset) that requires the status check **`uki`**, with GitHub Actions as its expected source.

5. Check where a commit stands at any time:

   ```sh
   uki status
   ```

## Commands

### `uki run [flags] [task...]`

Runs tasks at the repository root and records the result for `HEAD`.

- Without task names, runs the tasks of the first [branch rule](docs/configuration.md#branches) that matches the current branch.
- With task names, runs only those tasks ("task runner mode"). Their results are merged into the existing record for the commit, so tasks can be run one at a time.
- Tasks run sequentially in the listed order. All of them run even if one fails, unless `--fail-fast` is given.
- Both successes and failures are recorded.
- Files listed in a task's [`artifacts`](docs/configuration.md#artifacts) are stored with the record; read them back with `uki artifact get`.
- A task with [`inputs`](docs/configuration.md#inputs-and-reuse) is not run when a passing result for the same inputs is already recorded (for any commit); the result is recorded for `HEAD` as reused.

The result is **not** recorded (a warning is printed and the tasks still run) when:

- the working tree has staged, unstaged, or untracked changes before the run,
- a task leaves the working tree dirty (add generated files to `.gitignore`),
- `HEAD` moves while the tasks are running, or
- the run is interrupted.

| Flag | Description |
|---|---|
| `--branch <name>` | Branch name used for rule matching. Defaults to the current branch; required on a detached `HEAD` unless task names are given. |
| `--fail-fast` | Stop at the first failing task. |
| `--no-record` | Run without recording. Results are never reused in this mode. |
| `--no-reuse` | Run every task, even when a passing result for the same inputs is recorded. |

Exit code: `0` if every task passed, `1` if any task failed or uki itself failed, `2` for usage errors, `130` when interrupted.

### `uki status [flags]`

Shows the record and the verdict for a commit, using exactly the same evaluation as `uki mark`.

```
$ uki status
commit  1606b4d401f6 (main)
record  origin/uki/records:records/16/1606b4d401f6c6c84c3247d106d2878ab0baf680.json
  lint  ok (reused 7c1e0a94b2d3)       0s  2026-11-17 09:47:16  darwin/arm64
  test  ok                       211ms  2026-11-17 09:47:16  darwin/arm64
        artifact coverage.out (47.1 KiB)
status  success: Passed: lint, test
```

Reused results name the commit they came from, and stored [artifacts](docs/configuration.md#artifacts) are listed under their task.

It warns when the working tree is dirty, and when the local `uki.yaml` differs from the one on the remote default branch (`origin/HEAD`), because GitHub evaluates with the latter.

| Flag | Description |
|---|---|
| `--rev <commit>` | Commit to inspect. Default: `HEAD`. |
| `--branch <name>` | Branch name used for rule matching. Default: the current branch. |
| `--no-fetch` | Use the local copy of the records branch instead of fetching it. |

Exit code: `0` for success, `1` for failure or error, `3` for pending (no record yet, or required tasks missing). Handy in scripts and git hooks, e.g. a `pre-push` hook running `uki status || uki run`.

### `uki mark [flags]`

Sets commit statuses from records. Meant to run inside GitHub Actions (the workflow written by `uki init` calls it); use `--dry-run` to try it locally.

| Flag | Description |
|---|---|
| `--github-event` | Take the target from the GitHub Actions push event. A push to the records branch (`record.branch` in `uki.yaml`) judges every commit whose record was added or modified; a push to any other branch judges its head commit; a tag push is ignored. This is what the generated workflow uses. |
| `--sha <sha> --branch <name>` | Judge one commit as if it had been pushed to `<name>`. `--sha` must be a full commit SHA. |
| `--records-before <sha> --records-after <sha>` | Judge every commit whose record changed between two commits of the records branch. |
| `--dry-run` | Print verdicts instead of calling the GitHub API. |
| `--output <format>` | `text` (default) prints one line per judged commit. `json` prints, once at the end, an array of `{"sha", "branch", "state", "description"}`, for a later workflow step (e.g. to fetch artifacts of the commits that passed). |

Pass exactly one of `--github-event`, `--sha`, or `--records-after`.

Environment: `GITHUB_TOKEN` (needs the `statuses: write` permission) and `GITHUB_REPOSITORY`; with `--github-event` also `GITHUB_REF`, `GITHUB_SHA`, `GITHUB_EVENT_NAME` (must be `push`) and `GITHUB_EVENT_PATH`; optionally `GITHUB_API_URL`, `GITHUB_SERVER_URL` and `GITHUB_RUN_ID` (used to link the status to the workflow run). All are provided by GitHub Actions.

A commit that GitHub does not know yet is skipped with a message; the later push of that commit marks it.

### `uki artifact get [flags] <task> <path>`

Prints an [artifact](docs/configuration.md#artifacts) that `<task>` stored with the record of a commit. The content is checked against the SHA-256 and size in the record, and, when `record.allowed_signers` is set, the record's signature is verified the same way `uki mark` does, so the output is exactly what the recorder stored.

```sh
uki artifact get --rev "$GITHUB_SHA" -o coverage.out test coverage.out
```

| Flag | Description |
|---|---|
| `--rev <commit>` | Commit whose record holds the artifact. Default: `HEAD`. A full SHA does not need to exist locally, so a shallow checkout of the default branch works. |
| `-o <file>` | Write to this file (creating parent directories) instead of stdout. |
| `--no-fetch` | Use the local copy of the records branch instead of fetching it. |

Exit code: `0` on success; `1` when there is no record, the task or artifact is not in it, or the content does not match; `2` for usage errors.

### `uki init`

Writes `uki.yaml` and `.github/workflows/uki.yml` if they do not exist yet.

### `uki config get <key>`

Prints one setting from the working tree's `uki.yaml`, with defaults applied, for use in scripts and git hooks:

```sh
$ uki config get record.branch
uki/records
```

Keys: `record.branch`, `record.remote`, `record.sign`, `record.allowed_signers`, `record.max_artifact_bytes`, `status.context`. Unset values print an empty line. Exit code: `0` on success, `1` for an unknown key or an unreadable `uki.yaml`, `2` for usage errors.

### `uki version`

Prints the version.

## How a commit is judged

`uki mark` and `uki status` read `uki.yaml` **from the default branch** (the workflow checks it out), so a work branch cannot loosen its own requirements by editing `uki.yaml`.

For a commit pushed to a branch, the required tasks are those of the first branch rule matching that branch. Then:

| Situation | Status |
|---|---|
| No record for the commit | `pending` |
| A required task has not been run yet | `pending` |
| A required task failed | `failure` |
| Every required task passed | `success` |
| No branch rule matches the branch | `pending` (add a rule, e.g. a catch-all `"**"`, to cover it) |
| The matching rule lists no tasks (`tasks: []`) and a record exists | `success` |
| The record's tree does not match the commit | `error` |
| A task ran with a different `uki.yaml` than the one in the commit | `error` |
| A reused result does not verify with the default branch's `inputs` | `error` |
| Signature verification is enabled and fails | `error` |

On a push to the records branch, the branch name is taken from the record itself: the branch of the most recently started task.

## Records

Records live on an orphan branch (default `uki/records`) that shares no history with your code. Each commit gets one JSON file at `records/<first two hex chars>/<sha>.json`:

```json
{
  "sha": "1606b4d401f6c6c84c3247d106d2878ab0baf680",
  "tree": "0f52e5cea8673d4e8544d68baee113af079b5fa6",
  "tasks": {
    "test": {
      "command": "go test ./...",
      "success": true,
      "exit_code": 0,
      "branch": "main",
      "config_sha256": "e1cba395...",
      "started_at": "2026-11-17T00:47:16.123Z",
      "duration_ms": 211,
      "log_sha256": "12a61f4e...",
      "os": "darwin",
      "arch": "arm64",
      "uki_version": "v0.1.0"
    }
  }
}
```

`config_sha256` is the SHA-256 of `uki.yaml` at the recorded commit; `log_sha256` is the SHA-256 of the task's combined stdout and stderr. The output itself is not stored.

A task with [`inputs`](docs/configuration.md#inputs-and-reuse) also records `inputs_sha256`, its inputs key; a reused result adds `reused_from`, the commit the task really ran on (see the link for the `inputs/` index that makes reuse possible).

A task with [artifacts](docs/configuration.md#artifacts) also lists them in its result, keyed by their repository-relative path:

```json
      "artifacts": {
        "coverage.out": { "sha256": "9f2c41d0...", "size": 48213 }
      }
```

The files themselves are stored uncompressed (git compresses objects anyway) at `artifacts/<first two hex chars>/<sha>/<task>/<path>`, in the same commit as the record, so a record signature also vouches for the digests. `uki mark` does not look at artifacts; `uki artifact get` checks them when reading.

The record commit message summarizes the result with trailers (`Uki-Target`, `Uki-Tree`, `Uki-Task`), so `git log uki/records` doubles as an audit log.

Records are pushed with `git push --no-verify`: the records branch holds only generated JSON, and running your own `pre-push` hook for it would re-enter hooks that call uki.

Several developers can record at the same time: uki retries rejected pushes with a jittered backoff, re-reading and re-merging the record each time.

When uki creates the records branch it copies `.github/workflows/uki.yml` and, if present, the [workflow lockfile](https://docs.github.com/en/actions/concepts/security/workflow-dependency-lockfiles) `.github/workflows/actions.lock` onto it, because pushes to the records branch run the workflow definition found on that branch. Later changes to those files are **not** propagated automatically; update the records branch by hand when you change them.

## Signing records

With `record.sign: true`, record commits are created with `git commit -S`. With `record.allowed_signers` set, `uki mark` additionally requires the latest commit touching a record to carry a valid **SSH** signature from a key listed in that file (read from the default branch). Otherwise the status becomes `error`.

```sh
# each developer
git config gpg.format ssh
git config user.signingkey ~/.ssh/id_ed25519.pub
```

```
# .uki/allowed_signers on the default branch (ssh-keygen ALLOWED SIGNERS format)
alice@example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI...
bob@example.com   ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI...
```

GPG signatures are rejected on purpose: `git verify-commit` accepts a GPG signature from any key in the verifier's keyring, which would make the allow list meaningless. See [`examples/signed`](examples/signed/).

## Security model

uki moves *execution* from a trusted CI service to developers' machines, so the result is ultimately as trustworthy as the people recording it. A signature proves **who** recorded a result and that it was not altered afterwards. It does not prove the tasks really passed. uki is a fit for teams that trust their members, not for verifying contributions from untrusted parties (forks cannot record to the upstream repository anyway).

Within that model, protect the moving parts:

- **The workflow file.** A push-triggered workflow runs the definition contained in the pushed commit, so anyone who can push can rewrite it. Protect `.github/workflows/uki.yml` with CODEOWNERS and required reviews, or enforce the workflow with an organization ruleset ("Require workflows to pass").
- **The status itself.** Require the `uki` status check and restrict its expected source to GitHub Actions, so that statuses created directly through the API by a user token do not count.
- **Action dependencies.** Pin the actions used by the workflow with a [workflow lockfile](https://docs.github.com/en/actions/concepts/security/workflow-dependency-lockfiles) (`gh actions-lock`) or with commit SHAs.
- **The records branch.** Consider a ruleset that requires signed commits on `uki/records` and blocks force pushes.

## Development

uki checks itself with uki (see [`uki.yaml`](uki.yaml)):

```sh
go run ./cmd/uki run
```

The tasks are `golangci-lint run`, `go test -race ./...`, and `gh actions-lock --verify-local` (requires `gh extension install github/gh-actions-lock`).

### Before opening a pull request

**Always run `uki run` on the final commit before opening (or updating) a pull request.** The `uki` commit status, and therefore the merge check, comes only from these records.

Separately from uki, [`.github/workflows/ci.yml`](.github/workflows/ci.yml) runs tests and lint on GitHub Actions for pushes to `main`, pull requests opened by tagpr, and pull requests that change `.github/workflows/`. For tagpr's pull requests to trigger it, set the `TAGPR_TOKEN` secret (PAT or GitHub App token); pull requests created with the default `GITHUB_TOKEN` do not trigger workflows.

1. Commit everything. The working tree must be clean, or the result is not recorded.
2. Run `go run ./cmd/uki run` and make sure every task passes. The record is pushed to `uki/records` automatically.
3. Push the branch and open the pull request.

Any new `HEAD` (amend, rebase, extra commit, merge of `main`) needs a fresh `uki run` before pushing it.

