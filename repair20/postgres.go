package repair20

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"runtime-app/repair17"
	"runtime-app/repair19"
)

type PostgresLedger struct {
	Store  *repair19.Store
	Schema string
}

func (p PostgresLedger) Ensure(
	ctx context.Context,
	r repair19.Ref,
	source,
	base string,
	max int,
) (repair17.Workspace, error) {

	var zero repair17.Workspace

	if p.Store == nil || r.ID == "" {
		return zero, errors.New(
			"missing patch store/run id",
		)
	}

	schema := p.Schema

	if schema == "" {
		schema = "day19"
	}

	if !safeSchema(schema) ||
		len(schema) > 63 {
		return zero, errors.New(
			"invalid patch schema",
		)
	}

	var exists bool

	err := p.Store.Pool.QueryRow(
		ctx,
		"SELECT EXISTS(SELECT 1 FROM "+
			schema+
			".repair_runs WHERE run_id=$1)",
		r.ID,
	).Scan(&exists)

	if err != nil {
		return zero, err
	}

	// Already registered: do NOT create
	// a new Worktree after takeover.
	if exists {
		return p.Store.BoundWorktree(ctx, r)
	}

	root, err := filepath.Abs(source)
	if err != nil {
		return zero, err
	}

	base, err = filepath.Abs(base)
	if err != nil {
		return zero, err
	}

	validRunID := regexp.MustCompile(
		`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`,
	)

	if !validRunID.MatchString(r.ID) {
		return zero, errors.New("invalid RunID")
	}

	path := filepath.Join(base, r.ID)

	var w repair17.Workspace

	_, err = os.Lstat(path)

	switch {
	case err == nil:
		// Possible crash after creating
		// Worktree but before DB Register.
		w, err = repair17.OpenManaged(
			ctx, path,
		)
		if err != nil {
			return zero, err
		}

		if w.RunID != r.ID ||
			w.Source != root {
			return zero, repair19.ErrConflict
		}

	case errors.Is(err, os.ErrNotExist):
		w, err = repair17.Prepare(
			ctx, root, base, r.ID,
		)
		if err != nil {
			return zero, err
		}

	default:
		return zero, err
	}

	if err = p.Store.Register(
		ctx,
		r,
		w.Path,
		max,
	); err != nil {
		return zero, err
	}

	return p.Store.BoundWorktree(ctx, r)
}

func (p PostgresLedger) Recoverable(
	ctx context.Context,
	r repair19.Ref,
) ([]repair19.Intent, error) {
	return p.Store.Recoverable(ctx, r)
}

func (p PostgresLedger) Summary(
	ctx context.Context,
	id string,
) (int, int, error) {
	return p.Store.Summary(ctx, id)
}

func (p PostgresLedger) Prepare(
	ctx context.Context,
	r repair19.Ref,
	id string,
	x repair17.Patch,
) (repair19.Intent, error) {
	return p.Store.Prepare(ctx, r, id, x)
}

func (p PostgresLedger) ApplyOrRecover(
	ctx context.Context,
	r repair19.Ref,
	id string,
	h func(),
) (repair19.Outcome, error) {
	return p.Store.ApplyOrRecover(
		ctx, r, id, h,
	)
}

func (p PostgresLedger) Verify(
	ctx context.Context,
	r repair19.Ref,
	id string,
	timeout time.Duration,
) (repair17.TestResult, error) {
	return p.Store.Verify(
		ctx, r, id, timeout,
	)
}

func RequireTestDatabase(
	ctx context.Context,
	p *repair19.Store,
) error {
	if p == nil {
		return errors.New("nil patch store")
	}

	var name string

	err := p.Pool.QueryRow(
		ctx,
		"SELECT current_database()",
	).Scan(&name)

	if err != nil {
		return err
	}

	if name != "agent_runtime_day14_test" {
		return fmt.Errorf(
			"refusing database %q",
			name,
		)
	}

	return nil
}

func safeSchema(v string) bool {
	for i, c := range v {
		if !((c >= 'a' && c <= 'z') ||
			(i > 0 &&
				(c >= '0' && c <= '9')) ||
			c == '_') {
			return false
		}
	}

	return len(v) > 0
}
