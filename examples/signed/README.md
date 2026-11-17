# Signed records

With signing enabled, a status reflects a record only if the record was committed by someone on your allow list. Without it, anyone who can push to the records branch can write any record.

A signature proves who recorded a result. It does not prove the tasks really passed (see the [security model](../../README.md#security-model)).

## Files

- [`uki.yaml`](uki.yaml): enables `record.sign` and `record.allowed_signers`.
- [`allowed_signers`](allowed_signers): template for `.uki/allowed_signers`.

## Rollout

Enabling verification before everyone signs turns every status into `error`, so go in this order.

1. **Every developer** configures SSH commit signing:

   ```sh
   git config --global gpg.format ssh
   git config --global user.signingkey ~/.ssh/id_ed25519.pub
   ```

   Only SSH signatures are accepted. GPG signatures are rejected because `git verify-commit` trusts any key in the verifier's keyring.

2. **Collect the public keys** into `.uki/allowed_signers`, one line per person, with the email address they commit with:

   ```
   alice@example.com ssh-ed25519 AAAAC3Nza...
   ```

3. **Merge `uki.yaml` and `.uki/allowed_signers` into the default branch.** `uki mark` reads both from the default branch, so verification starts only once they are merged.

4. Run `uki run` as usual. Each record commit is signed, and `uki status` shows whether the signature verifies:

   ```
   $ uki status
   ...
   status  success: Passed: lint, test
   ```

   With an unknown key it reports:

   ```
   uki: signature check failed: commit 6f4827952698: signature status "U" (want G)
   status  error: Record signature could not be verified
   ```

## What is verified

For each commit being judged, uki looks at the most recent commit on the records branch that modified that commit's record. That commit must:

- carry an SSH signature, and
- verify as good (`git log --format=%G?` prints `G`) against `.uki/allowed_signers`.

To add or revoke a person, change `.uki/allowed_signers` on the default branch. Records signed by a revoked key turn into `error` the next time they are judged.
