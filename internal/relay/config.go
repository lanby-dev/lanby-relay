package relay

import (
	"os"
	"path/filepath"
	"strconv"
)

type Config struct {
	PlatformURL        string
	IdentityPath       string
	RelayVersion       string
	DefaultPollSeconds int
}

func LoadConfigFromEnv() Config {
	poll := 30
	if v := os.Getenv("CONFIG_POLL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			poll = n
		}
	}
	idPath := os.Getenv("IDENTITY_PATH")
	if idPath == "" {
		idPath = filepath.Join(".", "identity.json")
	}
	url := os.Getenv("PLATFORM_URL")
	if url == "" {
		url = "https://api.lanby.dev"
	}
	ver := os.Getenv("AGENT_VERSION")
	if ver == "" {
		ver = "0.1.0"
	}
	return Config{
		PlatformURL:        url,
		IdentityPath:       idPath,
		RelayVersion:       ver,
		DefaultPollSeconds: poll,
	}
}
