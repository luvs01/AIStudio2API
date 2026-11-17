package config

import (
	"fmt"
	"net"
	"strings"

	"github.com/Mag1cFall/AIStudio2API/internal/buildpolicy"
)

func validateConservative(c Config) error {
	if !buildpolicy.Conservative {
		return nil
	}
	for _, address := range []string{c.ListenAddr, c.PreviewListenAddr} {
		host, _, err := net.SplitHostPort(address)
		if err != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return fmt.Errorf("conservative trial requires literal loopback listeners")
		}
	}
	if !c.AdminAuthEnabled || strings.TrimSpace(c.AdminPassword) == "" {
		return fmt.Errorf("conservative trial requires separate management authentication")
	}
	switch {
	case c.WAABackend != WAABackendCamoufox:
		return fmt.Errorf("conservative trial requires WAA_BACKEND=camoufox")
	case c.FirstEventHedge != FirstEventHedgeOff:
		return fmt.Errorf("conservative trial requires FIRST_EVENT_HEDGE=off")
	case c.PerAccountConcurrency != 1:
		return fmt.Errorf("conservative trial requires PER_ACCOUNT_CONCURRENCY=1")
	case c.MaxActiveWorkers != 1:
		return fmt.Errorf("conservative trial requires MAX_ACTIVE_WORKERS=1")
	case c.WarmStartupConcurrency != 1:
		return fmt.Errorf("conservative trial requires WARM_STARTUP_CONCURRENCY=1")
	case c.WarmWorkerLimit > 1:
		return fmt.Errorf("conservative trial requires WARM_WORKER_LIMIT<=1")
	}
	if c.RequestBodyLog || c.FakeStream || c.StripThoughtSignatures || c.ContinuationRewrite || len(c.UpstreamChannels) != 1 || c.UpstreamChannels[0] != "playground" {
		return fmt.Errorf("conservative trial requires native Playground responses and disabled body logging")
	}
	return nil
}

// ValidateRuntimePolicy covers the credential-independent Config structure and
// the secret that must be present before the local service can start or persist.
func ValidateRuntimePolicy(c Config) error {
	if err := validateConservative(c); err != nil {
		return err
	}
	if buildpolicy.Conservative && strings.TrimSpace(c.ProxyAPIKey) == "" {
		return fmt.Errorf("conservative trial requires a local API key")
	}
	return nil
}
