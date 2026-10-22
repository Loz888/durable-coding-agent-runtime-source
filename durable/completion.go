package durable

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"runtime-app/repair19"
)

// FinalizeExhausted is evidence-only maintenance, NOT an execution claim.
// A successful Verify persisted completion_pending in the same transaction as
// its digest. Sweep universally skips this marker until inspection finishes.
// A fresh unexposed owner is granted and consumed inside ONE Run-row transaction;
// no Claim/Epoch/repair/model counter increases. Rollback leaves no authority.
func (h CodingHandler) FinalizeExhausted(ctx context.Context, q Queue, limit int) (int, error) {
	if h.Pool == nil || h.PatchStore == nil || q.Pool != h.Pool || limit < 1 || limit > 100 {
		return 0, errors.New("invalid completion maintenance")
	}
	rows, err := h.Pool.Query(ctx, `SELECT run_id FROM day12.agent_runs WHERE status='running' AND queue_enabled AND completion_pending AND lease_epoch>=max_attempts AND (lease_until IS NULL OR lease_until<=clock_timestamp()) AND execution_profile=$1 ORDER BY run_id LIMIT $2`, q.executionProfile(), limit)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range ids {
		ok, e := h.finalizeOne(ctx, q, id)
		if e != nil {
			return count, e
		}
		if ok {
			count++
		}
	}
	return count, nil
}

func (h CodingHandler) finalizeOne(ctx context.Context, q Queue, id string) (bool, error) {
	tx, err := h.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var job ClaimedRun
	job.RunID = id
	err = tx.QueryRow(ctx, `SELECT repo,provider,goal,lease_epoch FROM day12.agent_runs WHERE run_id=$1 AND status='running' AND queue_enabled AND completion_pending AND lease_epoch>=max_attempts AND (lease_until IS NULL OR lease_until<=clock_timestamp()) AND execution_profile=$2 FOR UPDATE SKIP LOCKED`, id, q.executionProfile()).Scan(&job.Repo, &job.Provider, &job.Goal, &job.Lease.Epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	proof, proofErr := h.PatchStore.VerifiedCompletion(ctx, tx, id, h.TestTimeout)
	// Missing/stale evidence is a failure, never fresh testing without authorization.
	if proofErr != nil && !errors.Is(proofErr, repair19.ErrConflict) && !errors.Is(proofErr, pgx.ErrNoRows) {
		return false, proofErr
	}
	source := job.Repo
	if h.ResolveRepo != nil {
		var e error
		source, e = h.ResolveRepo(ctx, job)
		if e != nil {
			return false, e
		}
	}
	if !IsCodingProvider(job.Provider) || proof.Source != source || filepath.Clean(proof.Path) != filepath.Join(h.WorktreeBase, id) || !h.acceptsFiles(proof.Files) {
		proofErr = repair19.ErrConflict
	}
	if proofErr == nil && h.ExpectedCommit != nil {
		expected, e := h.ExpectedCommit(ctx, job)
		if e != nil {
			return false, e
		}
		if expected != proof.Commit {
			proofErr = repair19.ErrConflict
		}
	}
	// Even a hostile stale caller cannot learn this transient owner from committed state.
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return false, err
	}
	owner := "completion-" + hex.EncodeToString(nonce[:])
	var until time.Time
	err = tx.QueryRow(ctx, `UPDATE day12.agent_runs SET lease_owner=$2,lease_until=clock_timestamp()+interval '3 seconds' WHERE run_id=$1 RETURNING lease_until`, id, owner).Scan(&until)
	if err != nil {
		return false, err
	}
	fenced := FencedStore{Pool: h.Pool, Lease: Lease{RunID: id, Owner: owner, Epoch: job.Lease.Epoch, Until: until}}
	snapshot, err := loadSnapshot(ctx, tx, id)
	if err != nil {
		return false, err
	}
	expected := snapshot.Revision
	snapshot.Revision++
	if proofErr == nil {
		snapshot.Status = Succeeded
		snapshot.OutcomeVerdict = "tests_passed"
		snapshot.Final = fmt.Sprintf("coding evidence-only completion; rounds=%d/%d; last_patch=%s; no execution claim/model call", proof.Rounds, proof.Max, proof.PatchID)
	} else {
		snapshot.Status = Failed
		snapshot.OutcomeVerdict = "not_evaluated"
		snapshot.Final = "coding completion refused: missing, stale or mismatched verification evidence; execution budget exhausted"
	}
	if err = fenced.save(ctx, snapshot, expected, tx); err != nil {
		return false, err
	}
	if proofErr != nil {
		if _, err = tx.Exec(ctx, `UPDATE day12.agent_runs SET failure_kind='retry_exhausted',failure_reason='completion evidence unavailable or stale; execution budget exhausted' WHERE run_id=$1`, id); err != nil {
			return false, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE day12.agent_runs SET lease_owner=NULL,lease_until=NULL WHERE run_id=$1`, id); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
