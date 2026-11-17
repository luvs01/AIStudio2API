//go:build conservative

package app

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

type conservativeMaintenanceCatalog struct {
	modelCatalogService
	models    []aistudio.Model
	refreshed int
	expired   int
	patrolled int
}

func (catalog *conservativeMaintenanceCatalog) RefreshAccountModels(context.Context, string, bool) ([]aistudio.Model, error) {
	catalog.refreshed++
	return catalog.models, nil
}

func (catalog *conservativeMaintenanceCatalog) CachedModels() []aistudio.Model {
	return catalog.models
}

func (catalog *conservativeMaintenanceCatalog) ReclaimExpiredResources(context.Context) error {
	catalog.expired++
	return nil
}

func (catalog *conservativeMaintenanceCatalog) RefreshAccountState(context.Context, string, bool) (aistudio.AccountRefresh, error) {
	catalog.patrolled++
	return aistudio.AccountRefresh{}, nil
}

func TestConservativeMaintenanceRecoversCatalogAndReclaims(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		catalog := &conservativeMaintenanceCatalog{models: []aistudio.Model{{ID: "fixture-model"}}}
		account := &aistudio.Account{ID: "fixture", Config: aistudio.DefaultAccountConfig("fixture"), State: aistudio.AccountReady}
		service := &trackedService{
			lifecycle: ctx, catalog: catalog, pool: aistudio.NewAccountPool([]*aistudio.Account{account}, 1),
			requests: newRequestRegistry(ctx), timeout: time.Minute,
			modelRetries: map[string]struct{}{account.ID: {}},
		}
		done := make(chan struct{})
		go func() {
			service.retryModelCatalogs(ctx, time.Now())
			close(done)
		}()
		synctest.Wait()
		if catalog.expired != 1 {
			t.Fatalf("startup cleanup: expired=%d", catalog.expired)
		}
		time.Sleep(modelCatalogRetryInterval)
		synctest.Wait()
		if catalog.refreshed != 1 || service.modelRetryPending(account.ID) || len(service.modelSnapshot()) != 1 {
			t.Fatalf("pending catalog did not recover: refreshed=%d pending=%t models=%d", catalog.refreshed, service.modelRetryPending(account.ID), len(service.modelSnapshot()))
		}
		time.Sleep(idleResourceReclaimInterval - modelCatalogRetryInterval)
		synctest.Wait()
		if catalog.expired != 2 || catalog.refreshed != 1 {
			t.Fatalf("periodic cleanup: expired=%d refreshed=%d", catalog.expired, catalog.refreshed)
		}
		time.Sleep(accountStateRefreshInterval)
		synctest.Wait()
		if got := service.catalogModelID(ctx, "unknown-model"); got != "unknown-model" {
			t.Fatalf("missing model lookup = %q", got)
		}
		if catalog.patrolled != 0 || catalog.refreshed != 1 {
			t.Fatalf("unsolicited account polling: patrolled=%d refreshed=%d", catalog.patrolled, catalog.refreshed)
		}
		cancel()
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("maintenance did not stop on cancellation")
		}
	})
}
