//go:build conservative

package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/api"
)

type conservativeAppletTransport struct {
	aistudio.ProtectedTransport
	send func() (*aistudio.RPCResponse, error)
}

func (transport conservativeAppletTransport) DoProtectedApplet(context.Context, aistudio.AppletProtectedRequest, aistudio.RPCRequest) (*aistudio.RPCResponse, error) {
	return transport.send()
}

func TestConservativeRejectsBrowserImportEntries(t *testing.T) {
	admin := &runtimeAdmin{}
	_, discoveryErr := admin.CookieBrowsers(t.Context(), "")
	_, cookieErr := admin.DiscoverCookieAccounts(t.Context(), api.CookieImportInput{Cookies: "must-not-be-parsed"})
	_, importErr := admin.ImportAccountState(t.Context(), api.AccountStateInput{})
	for name, err := range map[string]error{"discovery": discoveryErr, "cookies": cookieErr, "state": importErr} {
		if err == nil || !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("%s reached browser/import processing: %v", name, err)
		}
	}
}

func TestConservativeAuthenticationRequiresExplicitLogin(t *testing.T) {
	for _, name := range []string{"rpc", "applet", "direct_recovery"} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			storage := filepath.Join(directory, "storage-state.json")
			state := aistudio.StorageState{Cookies: []aistudio.StateCookie{{Name: "SAPISID", Value: "fixture", Domain: ".google.com", Path: "/"}}}
			state.SetAuthExtension(aistudio.AuthExtension{Source: aistudio.AuthSource{Browser: "camoufox", Email: "fixture@example.invalid"}})
			if err := aistudio.ValidateRuntimeState(state); err != nil {
				t.Fatal(err)
			}
			if err := aistudio.WriteStorageState(storage, state); err != nil {
				t.Fatal(err)
			}
			account := &aistudio.Account{
				ID: "fixture@example.invalid", Directory: directory, StoragePath: storage,
				RuntimePath: filepath.Join(directory, "runtime-state.json"), StorageState: state,
				Config: aistudio.DefaultAccountConfig("fixture@example.invalid"), State: aistudio.AccountReady,
			}
			pool := aistudio.NewAccountPool([]*aistudio.Account{account}, 1)
			lease, err := pool.AcquireAccount(t.Context(), account.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.Release()
			ctx := aistudio.ContextWithAccountLease(t.Context(), lease)
			refresher := &authRuntimeRefresher{
				pool: pool, requests: newRequestRegistry(t.Context()), chromeHas: chromeIdentityImportable,
				refresh: func(context.Context, aistudio.AuthSource, aistudio.ChromeOAuthMaterial, string) ([]aistudio.StateCookie, *aistudio.BoundSession, error) {
					t.Fatal("automatic OAuth refresh reached")
					return nil, nil, nil
				},
				importCurrent: func(context.Context, aistudio.AuthSource, *aistudio.ChromeOAuthMaterial, string) (aistudio.StorageState, error) {
					t.Fatal("Chrome state import reached")
					return aistudio.StorageState{}, nil
				},
				rotate: func(context.Context, aistudio.StorageState, aistudio.AuthExtension, string) ([]string, error) {
					t.Fatal("automatic bound-cookie renewal reached")
					return nil, nil
				},
			}
			cause := &aistudio.RPCError{StatusCode: http.StatusUnauthorized}
			if name == "direct_recovery" {
				err = refresher.Recover(ctx, cause, lease.AuthGeneration())
			} else {
				calls := 0
				send := func() (*aistudio.RPCResponse, error) {
					calls++
					return nil, cause
				}
				if name == "applet" {
					transport := &authRetryProtectedTransport{transport: conservativeAppletTransport{send: send}, refresher: refresher}
					_, err = transport.DoProtectedApplet(ctx, aistudio.AppletProtectedRequest{}, aistudio.RPCRequest{Method: "createApplet"})
				} else {
					_, err = refresher.do(ctx, "generateContent", send)
				}
				if calls != 1 {
					t.Fatalf("RPC attempts = %d, want 1", calls)
				}
			}
			if !errors.Is(err, cause) || pool.Status()[0].State != aistudio.AccountAuthRequired {
				t.Fatalf("authentication failure not retained: err=%v status=%+v", err, pool.Status())
			}
			stored, err := aistudio.LoadStorageState(storage)
			if err != nil {
				t.Fatal(err)
			}
			extension, exists, err := stored.AuthExtension()
			if err != nil || !exists || extension.Source.Browser != "camoufox" || extension.OAuth != nil || extension.Bound != nil || stored.Cookies[0].Value != "fixture" {
				t.Fatalf("local sign-in state changed: extension=%+v err=%v", extension, err)
			}
		})
	}
}

func TestConservativeAllowsOnlyQuotaAccountFallback(t *testing.T) {
	if authenticationRecoveryAllowed(0) {
		t.Fatal("authentication recovery enabled")
	}
	if !generationAttemptAllowed(0, nil) {
		t.Fatal("first generation disabled")
	}
	planLimit := &aistudio.RPCError{StatusCode: http.StatusTooManyRequests, Message: "You have reached your usage limit"}
	dailyLimit := &aistudio.RPCError{StatusCode: http.StatusTooManyRequests, Metadata: map[string]string{"quota_metric": "generate_requests_per_day"}}
	for name, test := range map[string]struct {
		err     error
		allowed bool
	}{
		"plan":       {planLimit, true},
		"daily":      {dailyLimit, true},
		"wrapped":    {fmt.Errorf("attempt failed: %w", planLimit), true},
		"network":    {errors.New("connection reset"), false},
		"auth":       {&aistudio.RPCError{StatusCode: http.StatusUnauthorized}, false},
		"server":     {&aistudio.RPCError{StatusCode: http.StatusBadGateway}, false},
		"shortQuota": {&aistudio.RPCError{StatusCode: http.StatusTooManyRequests, RetryDelay: time.Minute}, false},
		"noFailure":  {nil, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := generationAttemptAllowed(1, test.err); got != test.allowed {
				t.Fatalf("generation retry allowed = %v, want %v", got, test.allowed)
			}
		})
	}
	if !generationAttemptAllowed(1, planLimit) || generationAttemptAllowed(2, errors.New("connection reset")) {
		t.Fatal("an earlier quota failure authorized a later non-quota replay")
	}
}
