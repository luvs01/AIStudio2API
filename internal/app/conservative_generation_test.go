//go:build conservative

package app

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

// The real retry loop and account pool run here; browser startup and upstream
// generation are replaced at their boundaries, with no network transport.
type conservativeGenerationService struct {
	aistudio.Service
	generated []aistudio.GenerateRequest
	copiedTo  []string
}

func (service *conservativeGenerationService) Generate(_ context.Context, request aistudio.GenerateRequest) (<-chan aistudio.Event, error) {
	service.generated = append(service.generated, request)
	events := make(chan aistudio.Event, 2)
	events <- aistudio.Event{Kind: aistudio.EventText, Part: aistudio.Part{Text: "fixture reply"}, ProviderModel: request.Model}
	events <- aistudio.Event{Kind: aistudio.EventFinish, FinishReason: "STOP", ProviderModel: request.Model}
	close(events)
	return events, nil
}

func (service *conservativeGenerationService) CopyFileReferencesToLease(_ context.Context, lease *aistudio.AccountLease, contents []aistudio.Content) ([]aistudio.Content, *aistudio.TemporaryFileCopies, error) {
	service.copiedTo = append(service.copiedTo, lease.Account().ID)
	copied := slices.Clone(contents)
	for index := range copied {
		copied[index].Parts = slices.Clone(copied[index].Parts)
		for part := range copied[index].Parts {
			if original := copied[index].Parts[part].File; original != nil {
				file := *original
				file.ID = "copied-to-" + lease.Account().ID
				copied[index].Parts[part].File = &file
			}
		}
	}
	return copied, &aistudio.TemporaryFileCopies{}, nil
}

func conservativeFileGenerationFixture(t *testing.T, singleAccount bool) (*trackedService, *conservativeGenerationService, aistudio.GenerateRequest, string) {
	t.Helper()
	owner := &aistudio.Account{ID: "owner@example.invalid", Config: aistudio.DefaultAccountConfig("owner"), State: aistudio.AccountReady}
	alternate := &aistudio.Account{ID: "alternate@example.invalid", Config: aistudio.DefaultAccountConfig("alternate"), State: aistudio.AccountReady}
	accounts := []*aistudio.Account{owner}
	if !singleAccount {
		accounts = append(accounts, alternate)
	}
	pool := aistudio.NewAccountPool(accounts, 1)
	pool.SetUpstreamChannels([]aistudio.Channel{aistudio.ChannelPlayground})
	model := aistudio.Model{ID: "fixture-model", Methods: []string{"generateContent"}, Capabilities: map[string]bool{"chat_model": true}}
	otherModel := model
	otherModel.ID = "owner-only-model"
	if singleAccount {
		otherModel = model
	}
	if err := pool.SetCatalog(owner.ID, owner.BenefitTier, []aistudio.Model{otherModel}); err != nil {
		t.Fatal(err)
	}
	if !singleAccount {
		if err := pool.SetCatalog(alternate.ID, alternate.BenefitTier, []aistudio.Model{model}); err != nil {
			t.Fatal(err)
		}
	}
	const resourceID = "fixture-file"
	if err := pool.BindResourceKind(resourceID, owner.ID, "file"); err != nil {
		t.Fatal(err)
	}
	upstream := &conservativeGenerationService{}
	service := &trackedService{
		lifecycle: t.Context(), service: upstream, pool: pool, requests: newRequestRegistry(t.Context()),
		workers: &accountWorkerManager{accounts: make(map[string]*accountWorker)}, models: []aistudio.Model{model},
	}
	request := aistudio.GenerateRequest{
		ID: "file-selection-fixture", Model: model.ID,
		Contents: []aistudio.Content{{Role: aistudio.RoleUser, Parts: []aistudio.Part{{File: &aistudio.FileRef{ID: resourceID}}}}},
	}
	return service, upstream, request, resourceID
}

func acquireConservativeFixtureLease(pool *aistudio.AccountPool, ctx context.Context, selection aistudio.AccountSelection) (*aistudio.AccountLease, error) {
	lease, _, err := pool.TryAcquireFor(ctx, selection)
	if lease == nil && err == nil {
		err = pool.NoEligibleError(selection)
	}
	return lease, err
}

func TestConservativeFileSelectionCopiesBeforeFirstGeneration(t *testing.T) {
	service, upstream, request, resourceID := conservativeFileGenerationFixture(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var selectedResources []string
	acquire := func(ctx context.Context, selection aistudio.AccountSelection, _ bool, _ *int, _ *time.Duration) (*aistudio.AccountLease, error) {
		selectedResources = append(selectedResources, selection.ResourceID)
		return acquireConservativeFixtureLease(service.pool, ctx, selection)
	}
	events := make(chan aistudio.Event, 8)
	service.generateWithRetry(t.Context(), ctx, cancel, time.Now(), request, resourceID, events, acquire)
	var kinds []aistudio.EventKind
	for event := range events {
		if event.Kind == aistudio.EventError {
			t.Fatalf("file preparation emitted an error before generation: %v; selections=%v generated=%d", event.Err, selectedResources, len(upstream.generated))
		}
		kinds = append(kinds, event.Kind)
	}
	if !slices.Equal(selectedResources, []string{resourceID, ""}) {
		t.Fatalf("resource selections = %v, want owner then copy target", selectedResources)
	}
	if !slices.Equal(upstream.copiedTo, []string{"alternate@example.invalid"}) || len(upstream.generated) != 1 {
		t.Fatalf("copies=%v generation calls=%d, want one copy and one generation on alternate", upstream.copiedTo, len(upstream.generated))
	}
	generated := upstream.generated[0]
	if generated.AccountID != "alternate@example.invalid" || generated.Contents[0].Parts[0].File.ID != "copied-to-alternate@example.invalid" {
		t.Fatalf("generation did not use the alternate account's copy: %+v", generated)
	}
	if !slices.Equal(kinds, []aistudio.EventKind{aistudio.EventText, aistudio.EventFinish}) {
		t.Fatalf("event sequence = %v", kinds)
	}
}

func TestConservativeFileSelectionFailureTerminates(t *testing.T) {
	for _, name := range []string{"no_eligible_copy_target", "cancelled_copy_selection", "single_cooling_account"} {
		t.Run(name, func(t *testing.T) {
			singleAccount := name == "single_cooling_account"
			service, upstream, request, resourceID := conservativeFileGenerationFixture(t, singleAccount)
			cancelled := name == "cancelled_copy_selection"
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			calls := 0
			acquire := func(ctx context.Context, selection aistudio.AccountSelection, _ bool, _ *int, _ *time.Duration) (*aistudio.AccountLease, error) {
				calls++
				if singleAccount {
					// Keep the real one-account attempt budget, without waiting for
					// the scheduler's cooldown timer at the acquisition boundary.
					return nil, &aistudio.AllCoolingError{ModelID: request.Model, Until: time.Now().Add(time.Hour), Reason: "fixture cooldown"}
				}
				if selection.ResourceID != "" {
					return acquireConservativeFixtureLease(service.pool, ctx, selection)
				}
				if cancelled {
					cancel()
					return nil, ctx.Err()
				}
				// The available target can disappear while preparing a file copy.
				service.pool.SetModelAccounts(map[string][]string{request.Model: {"owner@example.invalid"}})
				return acquireConservativeFixtureLease(service.pool, ctx, selection)
			}
			events := make(chan aistudio.Event, 8)
			service.generateWithRetry(t.Context(), ctx, cancel, time.Now(), request, resourceID, events, acquire)
			want := aistudio.ErrNoEligibleAccount
			if cancelled {
				want = context.Canceled
			}
			count := 0
			for event := range events {
				count++
				matches := errors.Is(event.Err, want)
				if singleAccount {
					var cooling *aistudio.AllCoolingError
					matches = errors.As(event.Err, &cooling)
				}
				if event.Kind != aistudio.EventError || !matches {
					t.Fatalf("terminal event = %s/%v, want error %v", event.Kind, event.Err, want)
				}
			}
			if calls != 2 || count != 1 || len(upstream.generated) != 0 || len(upstream.copiedTo) != 0 {
				t.Fatalf("selections=%d events=%d generations=%d copies=%v", calls, count, len(upstream.generated), upstream.copiedTo)
			}
		})
	}
}
