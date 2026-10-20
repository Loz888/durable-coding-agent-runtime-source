package durable

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrLeaseBusy = errors.New(
		"agent run lease is held by another session",
	)
	ErrLeaseLost = errors.New(
		"agent run lease was lost or expired",
	)
)

type Lease struct {
	RunID string
	Owner string
	Epoch int64
	Until time.Time
}

type LeaseManager struct {
	Pool *pgxpool.Pool
}

func validateLeaseInput(
	id, owner string,
	ttl time.Duration,
) error {
	if strings.TrimSpace(id) == "" ||
		strings.TrimSpace(owner) == "" {
		return errors.New(
			"run ID and owner token are required",
		)
	}

	if ttl < time.Millisecond ||
		ttl > 24*time.Hour {
		return errors.New(
			"lease TTL must be between 1ms and 24h",
		)
	}

	return nil
}

// Claim 只有在租约不存在或已经过期时才能成功。
// 成功领取时，数据库原子地将 Epoch 加 1。
func (m LeaseManager) Claim(
	ctx context.Context,
	id, owner string,
	ttl time.Duration,
) (Lease, error) {
	if m.Pool == nil {
		return Lease{}, errors.New(
			"nil postgres pool",
		)
	}

	if err := validateLeaseInput(
		id, owner, ttl,
	); err != nil {
		return Lease{}, err
	}

	lease := Lease{
		RunID: id,
		Owner: owner,
	}

	err := m.Pool.QueryRow(ctx, `
		UPDATE day12.agent_runs
		SET
			lease_owner = $2,
			lease_epoch = lease_epoch + 1,
            first_claimed_at = COALESCE(first_claimed_at, clock_timestamp()),
			lease_until = clock_timestamp()
				+ ($3::bigint * interval '1 microsecond'),
			updated_at = clock_timestamp()
		WHERE run_id = $1
		  AND status = 'running'
                  AND queue_enabled = FALSE
                  AND lease_epoch < max_attempts
		  AND (
			lease_owner IS NULL
			OR lease_until <= clock_timestamp()
		  )
		RETURNING lease_epoch, lease_until
	`, id, owner, ttl.Microseconds()).Scan(
		&lease.Epoch,
		&lease.Until,
	)

	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, ErrLeaseBusy
	}

	if err != nil {
		return Lease{}, fmt.Errorf(
			"claim lease: %w", err,
		)
	}

	return lease, nil
}

// Renew 必须匹配当前 Owner 和 Epoch，且尚未过期。
func (m LeaseManager) Renew(
	ctx context.Context,
	lease Lease,
	ttl time.Duration,
) (time.Time, error) {
	if m.Pool == nil {
		return time.Time{}, errors.New(
			"nil postgres pool",
		)
	}

	if err := validateLeaseInput(
		lease.RunID,
		lease.Owner,
		ttl,
	); err != nil {
		return time.Time{}, err
	}

	var until time.Time

	err := m.Pool.QueryRow(ctx, `
		UPDATE day12.agent_runs
		SET
			lease_until = GREATEST(lease_until, clock_timestamp()
				+ ($4::bigint * interval '1 microsecond')),
			updated_at = clock_timestamp()
		WHERE run_id = $1
		  AND lease_owner = $2
		  AND lease_epoch = $3
		  AND status = 'running'
		  AND lease_until > clock_timestamp()
		RETURNING lease_until
	`,
		lease.RunID,
		lease.Owner,
		lease.Epoch,
		ttl.Microseconds(),
	).Scan(&until)

	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrLeaseLost
	}

	if err != nil {
		return time.Time{}, fmt.Errorf(
			"renew lease: %w", err,
		)
	}

	return until, nil
}

// Check 验证执行权。
func (m LeaseManager) Check(
	ctx context.Context,
	lease Lease,
) error {
	if m.Pool == nil {
		return errors.New(
			"nil postgres pool",
		)
	}

	var valid bool

	err := m.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM day12.agent_runs
			WHERE run_id = $1
			  AND lease_owner = $2
			  AND lease_epoch = $3
			  AND status = 'running'
			  AND lease_until > clock_timestamp()
		)
	`,
		lease.RunID,
		lease.Owner,
		lease.Epoch,
	).Scan(&valid)

	if err != nil {
		return fmt.Errorf(
			"check lease: %w", err,
		)
	}

	if !valid {
		return ErrLeaseLost
	}

	return nil
}

// Release 只释放与自己 Owner/Epoch 匹配的租约。
func (m LeaseManager) Release(
	ctx context.Context,
	lease Lease,
) error {
	if m.Pool == nil {
		return errors.New(
			"nil postgres pool",
		)
	}

	_, err := m.Pool.Exec(ctx, `
		UPDATE day12.agent_runs
		SET
			lease_owner = NULL,
			lease_until = NULL,
			updated_at = clock_timestamp()
		WHERE run_id = $1
		  AND lease_owner = $2
		  AND lease_epoch = $3
	`,
		lease.RunID,
		lease.Owner,
		lease.Epoch,
	)

	if err != nil {
		return fmt.Errorf(
			"release lease: %w", err,
		)
	}

	return nil
}
