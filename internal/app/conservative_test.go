//go:build conservative

package app

import (
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
)

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
