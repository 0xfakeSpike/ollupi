// Package catalog keeps what all shards of a deployment share: which shard each user is on, which process holds each
// shard now and where it is reached, and how busy each shard is. Shards hold their leases in it, routers find users'
// shards in it. SQL keeps it in a database of its own; a deployment that runs a coordination service, such as etcd,
// or a database served apart from the shards, implements Catalog over that instead.
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

// A Catalog decides each lease by its own clock, never by its callers': a lease runs out ttl after the catalog kept
// it, and a holder that counts ttl from before it asked never believes it holds the shard longer than the catalog does.
type Catalog interface {
	// Acquire takes the shard for owner when no process holds it or its holder let the lease run out, and publishes
	// owner's address with it, so that the process the address names is the one that holds the shard. Each taking of
	// a shard has an epoch above every one before it, which the shard's own database keeps to refuse an earlier
	// holder's writes.
	Acquire(ctx context.Context, shard, owner, address string, accepting bool, ttl time.Duration) (epoch int64, held bool, err error)
	// Renew keeps the lease of the epoch for ttl more and reports the shard's load with it, by a measure all shards
	// share; lower is lighter. It returns ErrLeaseLost once the epoch is not the shard's.
	Renew(ctx context.Context, shard, owner string, epoch int64, ttl time.Duration, load int64) error
	// Release gives the lease up at once, so that the next process need not wait out what is left of it.
	Release(ctx context.Context, shard, owner string, epoch int64) error

	// Locate places a user met for the first time on the lighter of two accepting shards drawn at random, and returns
	// their shard and the address of the process that holds it: "" while none does. A user stays on their shard.
	Locate(ctx context.Context, user string) (shard, address string, err error)
	// ShardOf returns "" for a user placed nowhere yet.
	ShardOf(ctx context.Context, user string) (string, error)
	// Place puts a user on a shard before they have anything, such as a customer's users on the shard kept for them;
	// it refuses a user already on another.
	Place(ctx context.Context, user, shard string) error
}
