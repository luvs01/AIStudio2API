//go:build conservative

package config

import (
	"strings"
	"testing"
)

func TestConservativeConfiguration(t *testing.T) {
	base := Default()
	base.ProxyAPIKey = "fixture"
	base.AdminPassword = "separate-management-secret"
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRuntimePolicy(base); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"public":      func(c *Config) { c.ListenAddr = "0.0.0.0:2048" },
		"preview":     func(c *Config) { c.PreviewListenAddr = "0.0.0.0:2050" },
		"adminAuth":   func(c *Config) { c.AdminAuthEnabled = false },
		"adminSecret": func(c *Config) { c.AdminPassword = "" },
		"hedge":       func(c *Config) { c.FirstEventHedge = "auto" },
		"concurrency": func(c *Config) { c.PerAccountConcurrency = 2 },
		"workers":     func(c *Config) { c.MaxActiveWorkers = 2 },
		"emulation":   func(c *Config) { c.WAABackend = WAABackendSpiderMonkey },
		"bodylog":     func(c *Config) { c.RequestBodyLog = true },
		"fakeStream":  func(c *Config) { c.FakeStream = true },
		"signatures":  func(c *Config) { c.StripThoughtSignatures = true },
		"rewrite":     func(c *Config) { c.ContinuationRewrite = true },
		"build":       func(c *Config) { c.UpstreamChannels = []string{"build"} },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			change(&cfg)
			if err := ValidateRuntimePolicy(cfg); err == nil {
				t.Fatal("restriction not enforced")
			}
		})
	}
	base.ProxyAPIKey = ""
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRuntimePolicy(base); err == nil {
		t.Fatal("empty API key accepted at runtime")
	}
}

func TestConservativeReportsInvalidWorkerSetting(t *testing.T) {
	for key, change := range map[string]func(*Config){
		"WAA_BACKEND":              func(c *Config) { c.WAABackend = WAABackendSpiderMonkey },
		"FIRST_EVENT_HEDGE":        func(c *Config) { c.FirstEventHedge = "auto" },
		"PER_ACCOUNT_CONCURRENCY":  func(c *Config) { c.PerAccountConcurrency = 2 },
		"MAX_ACTIVE_WORKERS":       func(c *Config) { c.MaxActiveWorkers = 2 },
		"WARM_STARTUP_CONCURRENCY": func(c *Config) { c.WarmStartupConcurrency = 2 },
		"WARM_WORKER_LIMIT":        func(c *Config) { c.WarmWorkerLimit = 2 },
	} {
		t.Run(key, func(t *testing.T) {
			cfg := Default()
			cfg.AdminPassword = "fixture-management-secret"
			cfg.ProxyAPIKey = "fixture-key"
			change(&cfg)
			err := ValidateRuntimePolicy(cfg)
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("expected actionable %s error, got %v", key, err)
			}
		})
	}
}
