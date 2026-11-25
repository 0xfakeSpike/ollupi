package sqlite

import (
	"errors"
	"reflect"
	"testing"

	"github.com/0xfakeSpike/ollupi/account"
	"github.com/0xfakeSpike/ollupi/pkg/llm"
	"github.com/0xfakeSpike/ollupi/store"
)

func TestTheCatalogIsReplacedWholeInOrderAndEveryUserHearsIt(t *testing.T) {
	s := open(t, t.TempDir())
	update(t, s, func(tx store.Tx) error { return errors.Join(tx.EnsureUser("u"), tx.EnsureUser("v")) })
	if got := read(t, s, func(r store.Reader) ([]account.ModelProvider, error) { return r.Catalog() }); len(got) != 0 {
		t.Fatalf("a new store's catalog: %+v", got)
	}
	off := false
	catalog := []account.ModelProvider{
		{ID: "zeta", API: account.OpenAI, BaseURL: "https://z/v1", APIKey: "sk-z", Models: []account.ProviderModel{
			{Name: "b", Calls: 2, Thinking: &off, Images: true, Limits: llm.Limits{Context: 1000, Output: 100, MaxInput: 800}},
			{Name: "a", Limits: llm.Limits{Context: 2000, Output: 200}},
		}},
		{ID: "alpha", API: account.Anthropic, Models: []account.ProviderModel{{Name: "c", Limits: llm.Limits{Context: 3000, Output: 300}}}},
	}
	heard := map[string]int{}
	for _, u := range []string{"u", "v"} {
		s.Feed().Subscribe(u, func(e store.Event) {
			if e.Owned != nil && e.Owned.Catalog != nil && e.User == "" && reflect.DeepEqual(*e.Owned.Catalog, catalog) {
				heard[u]++
			}
		})
	}
	update(t, s, func(tx store.Tx) error { return tx.SetCatalog(catalog) })
	if heard["u"] != 1 || heard["v"] != 1 {
		t.Fatalf("heard %v", heard)
	}
	if got := read(t, s, func(r store.Reader) ([]account.ModelProvider, error) { return r.Catalog() }); !reflect.DeepEqual(got, catalog) {
		t.Fatalf("the catalog read back:\n%+v\nwant\n%+v", got, catalog)
	}

	smaller := []account.ModelProvider{{ID: "alpha", API: account.Anthropic, APIKey: "k", Models: []account.ProviderModel{{Name: "c", Limits: llm.Limits{Context: 3000, Output: 300}}}}}
	update(t, s, func(tx store.Tx) error { return tx.SetCatalog(smaller) })
	if got := read(t, s, func(r store.Reader) ([]account.ModelProvider, error) { return r.Catalog() }); !reflect.DeepEqual(got, smaller) {
		t.Fatalf("the replaced catalog: %+v", got)
	}
	var left int64
	if err := s.g.Raw("SELECT count(*) FROM provider_models").Scan(&left).Error; err != nil || left != 1 {
		t.Fatalf("models left: %d, %v", left, err)
	}
}
