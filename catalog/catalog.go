package catalog

import (
	"context"
	"errors"
	"time"
)

var (
	ErrNoShard   = errors.New("catalog: no shard takes this user")
	ErrLeaseLost = errors.New("catalog: another process holds the shard")
)

type Catalog interface {
	Acquire(ctx context.Context, shard, owner, address string, accepting bool, ttl time.Duration) (epoch int64, held bool, err error)
	Renew(ctx context.Context, shard, owner string, epoch int64, ttl time.Duration, load int64) error
	Release(ctx context.Context, shard, owner string, epoch int64) error

	Locate(ctx context.Context, user string) (shard, address string, err error)
	ShardOf(ctx context.Context, user string) (string, error)
	Place(ctx context.Context, user, shard string) error
}
