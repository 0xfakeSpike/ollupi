package sqlite

import (
	"encoding/json"
	"fmt"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/store"
)

type providerRow struct {
	ID      string
	API     string
	BaseURL string
	APIKey  string
}

type providerModelRow struct {
	ProviderID string
	Name       string
	Calls      int
	Thinking   *bool
	Images     bool
	Limits     string
}

func (r reader) Catalog() ([]account.ModelProvider, error) {
	var providers []providerRow
	if err := r.g.Raw("SELECT id, api, base_url, api_key FROM providers ORDER BY position").Scan(&providers).Error; err != nil {
		return nil, fmt.Errorf("store: the catalog: %w", err)
	}
	var models []providerModelRow
	err := r.g.Raw(`SELECT m.provider_id, m.name, m.calls, m.thinking, m.images, m.limits FROM provider_models m
		JOIN providers p ON p.id = m.provider_id ORDER BY p.position, m.position`).Scan(&models).Error
	if err != nil {
		return nil, fmt.Errorf("store: the catalog's models: %w", err)
	}
	out := make([]account.ModelProvider, len(providers))
	for i, p := range providers {
		out[i] = account.ModelProvider{ID: p.ID, API: account.ModelAPI(p.API), BaseURL: p.BaseURL, APIKey: p.APIKey, Models: []account.ProviderModel{}}
		for _, m := range models {
			if m.ProviderID != p.ID {
				continue
			}
			pm := account.ProviderModel{Name: m.Name, Calls: m.Calls, Thinking: m.Thinking, Images: m.Images}
			if err := json.Unmarshal([]byte(m.Limits), &pm.Limits); err != nil {
				return nil, fmt.Errorf("store: the limits of %s: %w", account.Connection(p.ID, m.Name), err)
			}
			out[i].Models = append(out[i].Models, pm)
		}
	}
	return out, nil
}

// SetCatalog replaces the catalog whole: the providers and their models, in
// the order given. Every connection hears of it, as the catalog is the
// deployment's and not one user's.
func (t *tx) SetCatalog(providers []account.ModelProvider) error {
	if err := t.g.Exec("DELETE FROM providers").Error; err != nil {
		return fmt.Errorf("store: the catalog: %w", err)
	}
	for i, p := range providers {
		err := t.g.Exec("INSERT INTO providers(id, position, api, base_url, api_key) VALUES (?, ?, ?, ?, ?)",
			p.ID, i, string(p.API), p.BaseURL, p.APIKey).Error
		if err != nil {
			return fmt.Errorf("store: provider %s: %w", p.ID, err)
		}
		for j, m := range p.Models {
			limits, err := json.Marshal(m.Limits)
			if err != nil {
				return err
			}
			err = t.g.Exec("INSERT INTO provider_models(provider_id, position, name, calls, thinking, images, limits) VALUES (?, ?, ?, ?, ?, ?, ?)",
				p.ID, j, m.Name, m.Calls, m.Thinking, m.Images, string(limits)).Error
			if err != nil {
				return fmt.Errorf("store: model %s: %w", account.Connection(p.ID, m.Name), err)
			}
		}
	}
	catalog, err := t.Catalog()
	if err != nil {
		return err
	}
	t.owned = append(t.owned, store.Event{Owned: &store.Owned{Catalog: &catalog}})
	return nil
}
