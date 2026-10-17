# Patch protocol

[English](patch-protocol.md) | [简体中文](zh-CN/patch-protocol.md) | [Home](../README.md)

```text
prepared → applied → verified_pass / verified_fail

CurrentSHA == BeforeSHA → guarded Apply
CurrentSHA == AfterSHA  → RecoverApplied
otherwise              → Conflict; stop
```

Prepare persists path, hashes, exact replacement, preparing Epoch and causal
round_number, consuming one logical repair round in the same transaction.
Identical Patch ID/content is idempotent; different content under that ID
conflicts. New intents cannot overlap prepared, applied or verified_pass.
After verified_fail, a new intent needs budget and the preceding known AfterSHA.

The Store locks the Lease row and rechecks TTL after acquiring it.
ApplyGuarded checks authority immediately before Rename and before result
commit; a durably bound Worktree rejects older unguarded standalone Apply.
If expiry follows Rename, old result commit is rejected and the prepared
Intent remains recoverable.

VerifyGo runs outside the long transaction. Verdict persistence revalidates
Lease, Worktree binding and current hash. Verified results return cached output
and exit code without running tests again. DurationMS is not persisted in the
legacy table, so cached duration is not a fresh test measurement.

Identity combines a random Git-metadata token with baseline, registration and
path checks. It protects accidental path reuse in the stated trusted-writer
model, not malicious processes with equal OS access.
