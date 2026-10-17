# Execution lifecycle

[English](execution-lifecycle.md) | [简体中文](zh-CN/execution-lifecycle.md) | [Home](../README.md)

1. Submit atomically creates Run metadata and revision-zero history.
2. ClaimBatch selects priority/available candidates with FOR UPDATE SKIP LOCKED,
   checks lease_epoch < max_attempts, increments Epoch and assigns owner/expiry
   in one SQL statement. Manual non-queue Claim also enforces budget.
3. Heartbeat starts before slow model/workspace initialization. Renewal failure
   cancels the Run. Claim, Renew and guarded Patch writes share the Run row.
4. Diagnosis resumes persisted pending tools before another model call; model
   decisions are saved before tools and outputs in a later checkpoint.
   Coding reconciles persisted Patch state before proposing another change.
5. FencedStore.Save checks owner, Epoch, TTL and expected Revision, then commits
   a newer snapshot. CodingHandler writes the final Agent checkpoint.
6. Scheduler releases completed work. Terminal Runs are not reclaimable.
7. RetryLater / Day15 transient policy release and reschedule; permanent
   failures terminate. A stale owner/CAS error cannot finalize a new owner.
8. The same Scheduler periodically SweepExhausted with bounded batch/deadline.
   Released/NULL or expired exhausted Runs become terminal, including SIGKILL
   paths that never called RetryLater. Live leases and succeeded Runs remain
   protected; failure transition plus event is transactional/idempotent.

| Counter | Meaning | Consumed by |
|---|---|---|
| lease_epoch vs max_attempts | Successful execution grants | Successful Claim |
| retry_count | Explicit reschedules | RetryLater / retry policy |
| rounds_used vs max_rounds | New logical Patch intents | First Prepare |

MaxAttempts ≠ retry_count ≠ rounds_used. SIGKILL may consume a Claim without
an explicit retry. Takeover never resets repair budget; recovery does not
allocate a new Patch round. Maintenance defaults to a one-second cadence;
errors are logged and the existing Scheduler continues.
