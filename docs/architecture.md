# Architecture

[English](architecture.md) | [简体中文](zh-CN/architecture.md) | [Home](../README.md)

## Responsibilities and dependencies
runtimecore runs task.Task through executor.Registry with concurrent Workers
and result collection. persistence/httpapi provide a persistent task API.
This in-memory task runtime is distinct from durable Agent Run scheduling.

durable owns Queue, LeaseManager, FencedStore and Scheduler. Diagnosis providers
use durable.Runner plus repodiag. coding:* providers use CodingHandler and
repair20.Agent. Both reuse the same claims, heartbeat and Scheduler lifecycle;
the diagnosis model factory is not invoked for coding proposals.

Production dependencies: durable → repair20/repair19;
repair20 → repair19/repair18/repair17; repair19 → repair17.
internal/integration20 is test-only and references those packages one-way.
No production module imports it or needs a private-test export bridge.

## Schemas
Apply migrations in order: 001 creates day12.agent_runs/checkpoints; 002 adds
Lease owner/Epoch/expiry; 003 adds queue/priority/availability/retry_count;
004 adds budgets/failures/times/run_events; 005 creates day19 repair_runs and
patch_ops; 006 adds Worktree Identity and causal round_number.

HTTP persistence uses its own tasks schema through EnsureTaskSchema; tests
apply schema.sql in random runtime_test_* schemas.
Production events are unique by (run_id, revision, event_type); a legacy Day15
fixture has another shape, detected by Store. Equal-revision same-type
observations may merge; this is not one guaranteed event per attempt.

## Worktree lifecycle
repair17.Prepare creates a detached Git Worktree with Run/baseline metadata
and a random identity token. repair19.Register binds its absolute path, source
commit and identity to the logical Run. Recovery opens and validates that
same binding. Missing, symlinked, replaced or rebased workspaces fail closed.
Retain an unfinished Run's workspace for recovery; integration fixtures own
their temporary sources and cleanup.

## Persistence boundary
Short operations lock agent_runs, then repair_runs/patch_ops. Claim and Renew
contend on the same Run row. Guarded Rename is followed by authority/hash
validation before result commit. Model calls and Go tests run outside those
row-lock transactions. This is a trusted single-writer/shared-local-filesystem
model, not a sandbox or filesystem/database distributed transaction.

## Evaluation boundary

eval21.Score aggregates validated Observation records; cmd/day21-eval parses
manifests/JSONL and writes reports. Neither adds another Scheduler or Repair Loop.
TestDay21Capture drives the existing Scheduler with deterministic proposals and
collects independent baseline, Worktree-test, source-cleanliness and checkpoint evidence.
See [evaluation](evaluation.md).
