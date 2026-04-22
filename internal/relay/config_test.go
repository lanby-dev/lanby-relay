package relay

import "testing"

func TestLoadConfigFromEnv_Defaults(t *testing.T) {
	t.Setenv("CONFIG_POLL_SECONDS", "")
	t.Setenv("IDENTITY_PATH", "")
	t.Setenv("PLATFORM_URL", "")
	t.Setenv("AGENT_VERSION", "")
	t.Setenv("ALLOWED_PROBE_HOSTS", "")

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DefaultPollSeconds != 15 {
		t.Fatalf("expected default poll 15, got %d", cfg.DefaultPollSeconds)
	}
	if cfg.IdentityPath != "identity.json" {
		t.Fatalf("expected default identity path identity.json, got %q", cfg.IdentityPath)
	}
	if cfg.PlatformURL != "https://api.lanby.dev" {
		t.Fatalf("expected default platform URL, got %q", cfg.PlatformURL)
	}
	// Unset AGENT_VERSION falls back to the linker-settable Version (see version.go).
	if cfg.RelayVersion != Version {
		t.Fatalf("expected RelayVersion == Version (%q), got %q", Version, cfg.RelayVersion)
	}
}

func TestLoadConfigFromEnv_OverridesAndInvalidPoll(t *testing.T) {
	t.Setenv("CONFIG_POLL_SECONDS", "45")
	t.Setenv("IDENTITY_PATH", "/tmp/custom-identity.json")
	t.Setenv("PLATFORM_URL", "https://platform.example.com")
	t.Setenv("AGENT_VERSION", "1.2.3")
	t.Setenv("ALLOWED_PROBE_HOSTS", "")

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DefaultPollSeconds != 45 {
		t.Fatalf("expected poll 45, got %d", cfg.DefaultPollSeconds)
	}
	if cfg.IdentityPath != "/tmp/custom-identity.json" {
		t.Fatalf("expected custom identity path, got %q", cfg.IdentityPath)
	}
	if cfg.PlatformURL != "https://platform.example.com" {
		t.Fatalf("expected custom platform URL, got %q", cfg.PlatformURL)
	}
	if cfg.RelayVersion != "1.2.3" {
		t.Fatalf("expected custom relay version, got %q", cfg.RelayVersion)
	}

	t.Setenv("CONFIG_POLL_SECONDS", "not-a-number")
	cfg, err = LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DefaultPollSeconds != 15 {
		t.Fatalf("expected fallback poll 15 for invalid value, got %d", cfg.DefaultPollSeconds)
	}
}

func TestLoadConfigFromEnv_InvalidAllowedProbeHosts(t *testing.T) {
	t.Setenv("ALLOWED_PROBE_HOSTS", "not-a-cidr/99")
	_, err := LoadConfigFromEnv()
	if err == nil {
		t.Fatal("expected error for invalid ALLOWED_PROBE_HOSTS")
	}
}

func TestLoadConfigFromEnv_AllowedProbeHosts(t *testing.T) {
	t.Setenv("ALLOWED_PROBE_HOSTS", "*.local,192.168.0.0/16")
	cfg, err := LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.AllowedProbeHosts.Empty() {
		t.Fatal("expected non-empty allowlist")
	}
	if !cfg.AllowedProbeHosts.Allowed("http://mynas.local/") {
		t.Error("expected mynas.local to be allowed")
	}
	if cfg.AllowedProbeHosts.Allowed("http://other.example.com/") {
		t.Error("expected other.example.com to be blocked")
	}
}
