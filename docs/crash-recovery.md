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
