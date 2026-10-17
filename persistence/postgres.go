package persistence

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	Pool *pgxpool.Pool
}

func NewDB(
	ctx context.Context,
	dsn string,
) (*DB, error) {

	pool, err := pgxpool.New(
		ctx,
		dsn,
	)

	if err != nil {
		return nil, err
	}

	err = pool.Ping(ctx)

	if err != nil {
		pool.Close()
		return nil, err
	}

	return &DB{
		Pool: pool,
	}, nil
}
