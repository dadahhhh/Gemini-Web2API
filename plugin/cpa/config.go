package main

import (
	"fmt"
	"gopkg.in/yaml.v3"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const pluginID = "dadahhhh-gemini-web"
const version = "0.1.1"

type Config struct {
	Enabled               bool   `yaml:"enabled"`
	DataDir               string `yaml:"data_dir"`
	Proxy                 string `yaml:"proxy"`
	GuestEnabled          bool   `yaml:"guest_enabled"`
	ModelPrefix           string `yaml:"model_prefix"`
	PerAccountConcurrency int    `yaml:"per_account_concurrency"`
	RoutingStrategy       string `yaml:"routing_strategy"`
	AccountCooldown       string `yaml:"account_cooldown"`
	SessionTTL            string `yaml:"session_ttl"`
	InitTimeout           string `yaml:"init_timeout"`
	RequestTimeout        string `yaml:"request_timeout"`
}

func parseConfig(raw []byte) (Config, error) {
	c := Config{Enabled: true, DataDir: "data/" + pluginID, GuestEnabled: false, ModelPrefix: "gemini-web/", PerAccountConcurrency: 1, RoutingStrategy: "round-robin", AccountCooldown: "2m", SessionTTL: "30m", InitTimeout: "20s", RequestTimeout: "30m"}
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("invalid plugin YAML")
	}
	if strings.HasPrefix(c.DataDir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return c, err
		}
		c.DataDir = filepath.Join(home, c.DataDir[2:])
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return c, fmt.Errorf("data_dir is required")
	}
	c.DataDir = filepath.Clean(c.DataDir)
	if c.ModelPrefix == "" || !strings.HasSuffix(c.ModelPrefix, "/") || strings.ContainsAny(c.ModelPrefix, " \t\r\n?#") {
		return c, fmt.Errorf("model_prefix must be a nonempty prefix ending in /")
	}
	if c.PerAccountConcurrency < 1 || c.PerAccountConcurrency > 32 {
		return c, fmt.Errorf("per_account_concurrency must be 1..32")
	}
	if c.RoutingStrategy != "round-robin" && c.RoutingStrategy != "fill-first" {
		return c, fmt.Errorf("invalid routing_strategy")
	}
	for name, value := range map[string]string{"account_cooldown": c.AccountCooldown, "session_ttl": c.SessionTTL, "init_timeout": c.InitTimeout, "request_timeout": c.RequestTimeout} {
		d, err := time.ParseDuration(value)
		if err != nil || d <= 0 || d > 24*time.Hour {
			return c, fmt.Errorf("%s must be a positive duration <=24h", name)
		}
	}
	return c, nil
}

func duration(s string) time.Duration { d, _ := time.ParseDuration(s); return d }
