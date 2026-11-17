# Examples

Each directory contains a `uki.yaml` you can copy to the root of your repository. Every example is parsed and validated by uki's test suite, so they stay in sync with the configuration format.

| Example | Shows |
|---|---|
| [`go/`](go/) | A Go project: stricter rules for `main` and release branches, a catch-all for everything else. |
| [`node/`](node/) | A Node.js project: dependency installation as a task, environment variables, a docs-only branch rule. |
| [`monorepo/`](monorepo/) | Several components in one repository, with per-component branch prefixes. |
| [`signed/`](signed/) | Signed records verified against an SSH allowed signers file. |
| [`hooks/`](hooks/) | A git `pre-push` hook that makes sure the commit being pushed has a passing record. It reads the records branch name with `uki config get record.branch`. |

Whatever the example, you also need the GitHub workflow. Generate it with:

```sh
uki init   # writes uki.yaml (if missing) and .github/workflows/uki.yml
```

Then require the `uki` status check in your branch protection rule or ruleset. See the [main README](../README.md#quick-start) for the full setup and the [configuration reference](../docs/configuration.md) for every option.
