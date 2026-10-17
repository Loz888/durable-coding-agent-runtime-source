# Crash recovery

[English](crash-recovery.md) | [简体中文](zh-CN/crash-recovery.md) | [Home](../README.md)

SIGKILL prevents cleanup, release and final checkpoint writes. PostgreSQL may
still show running while the file already contains a Patch.

A replacement Worker must obtain a real new Claim after expiry. Epoch advances
only through Claim; old-owner writes are rejected even if that process resumes.
Revision CAS independently prevents overwriting one checkpoint version twice.

repair20.Agent first opens its bound Worktree and scans recoverable Patch
history ordered by causal round_number, not wall-clock created_at. Prepared
state is reconciled with file SHA; applied is verified; verified states reuse
persisted test evidence. Only failed verification plus remaining shared budget
allows another proposal.

A verified_pass Patch may survive without the final Agent checkpoint.
CodingHandler recovers it without a new model call and finishes through
FencedStore. Patch completion and Run completion are separate boundaries.

scripts/demo_crash_recovery.sh runs the four-state child SIGKILL matrix and a
true SIGKILL between verified_pass and final checkpoint. It verifies real
ClaimBatch takeover, fencing, cached verdicts, shared budget and source isolation.
Only that test Run's expiry is advanced to avoid a long wait; Epoch is never
synthesized. Only an owned child is killed, never the shell or database.

The Mock harness drives existing Scheduler.execute; the checkpoint-gap recovery
calls the same public CodingHandler. These are bounded integration demos, not
a daemon load benchmark. Rename and Commit remain separate; unknown file bytes,
missing/replaced Worktrees, hostile writers, power loss and multi-host recovery
are not made safe by declaring the demo successful.

## Distinct fault windows

| Experiment | Failure / takeover boundary | What it proves |
| --- | --- | --- |
| Day 19 legacy CLI | -inject-sigkill after Rename and before Patch result persistence | Intent/hash reconciliation; its legacy lab uses direct ownership changes rather than real Queue takeover |
| Day 19 Store unit test | TestDay19CrashBetweenRenameAndResult returns an injected hook error | The Rename/result gap is recoverable; this unit test is explicitly not a real SIGKILL |
| Four-state matrix | Owned child is killed after prepared, applied, verified_pass or verified_fail is durable | Recovery per committed state with actual ClaimBatch takeover |
| Day 20 checkpoint gap | TestDay20RealSIGKILLBeforeCheckpoint kills its own child after verified_pass, before final Agent checkpoint | Patch verdict survives and a real new owner finishes the checkpoint without another model call |
| Day 21 evaluation | Seed verified_pass, expire only the test lease, claim as Worker B | Deterministic cached-result takeover measurements; no SIGKILL is injected here |

The Rename/result gap and verified-result/final-checkpoint gap are different.
The current demo runs the real four-state matrix and Day 20 checkpoint test;
retained Day 19 CLI/unit results are not substituted for those real-claim tests.
