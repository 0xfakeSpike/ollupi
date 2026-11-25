package app

import (
	"context"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/store"
)

// Catalog is the deployment's catalog as stored, keys included, and the
// commit it was read at. It is the operator's to read: a user is only told
// the models (Models).
func (a *App) Catalog(ctx context.Context) (providers []account.ModelProvider, seq int64, err error) {
	err = a.st.Read(ctx, func(r store.Reader) error {
		if providers, err = r.Catalog(); err != nil {
			return err
		}
		seq, err = r.Seq()
		return err
	})
	return providers, seq, err
}

// SetCatalog replaces the deployment's catalog whole. One that does not
// check is refused (account.ErrInvalidModelProvider) and the catalog stays
// as it was; once stored, new turns run on it, and a turn already calling a
// model finishes on its client. A user's choice of a model no longer served
// falls back to the first.
func (a *App) SetCatalog(ctx context.Context, providers []account.ModelProvider) error {
	if err := account.CheckCatalog(providers); err != nil {
		return err
	}
	return a.st.Update(ctx, func(tx store.Tx) error { return tx.SetCatalog(providers) })
}
