// Package config is where Runwatch's settings come from: config.json in the
// data folder, overridden by RUNWATCH_* environment variables. It is the
// only package that knows where the data folder is, so a test that sets
// RUNWATCH_DATA_DIR can never touch the real one.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The environment overrides. Each wins over config.json.
const (
	EnvDataDir  = "RUNWATCH_DATA_DIR"     // data folder (config, history, log)
	EnvRoots    = "RUNWATCH_ROOTS"        // runner folders, separated like PATH
	EnvAddr     = "RUNWATCH_ADDR"         // listen address, e.g. 127.0.0.1:0
	EnvAPI      = "RUNWATCH_GITHUB_API"   // GitHub API base URL
	EnvToken    = "RUNWATCH_GITHUB_TOKEN" // one token for every repo, instead of gh
	EnvPollMS   = "RUNWATCH_POLL_MS"      // ask GitHub this often (milliseconds); for tests
	EnvServices = "RUNWATCH_SERVICES"     // "off": do not look for runner services, only Roots
)

// DefaultAddr is where the dashboard is served.
const DefaultAddr = "127.0.0.1:7878"

// Config is config.json.
type Config struct {
	// Roots are folders that hold runner folders, searched in addition to
	// the runners installed as Windows services.
	Roots []string `json:"roots"`
	// Addr is the address the dashboard listens on. Only loopback is accepted.
	Addr string `json:"addr"`
	// GitHubAPI is the API base URL; empty means https://api.github.com.
	GitHubAPI string `json:"githubApi,omitempty"`
	// Managed maps a pattern over a runner's folder name (billing-*) to the
	// reason Runwatch must not start or stop that service.
	Managed map[string]string `json:"managed"`
	// Services says whether the runners installed as Windows services are
	// looked for. Off, only Roots are searched (and no service can be
	// started or stopped).
	Services bool `json:"services"`
	// Notifications turns desktop notifications on or off.
	Notifications bool `json:"notifications"`

	// Token comes from RUNWATCH_GITHUB_TOKEN only; it is never read from or
	// written to a file.
	Token string `json:"-"`
	// Poll, when set (RUNWATCH_POLL_MS), replaces both polling intervals.
	Poll time.Duration `json:"-"`
	// DataDir is the folder this config was loaded from.
	DataDir string `json:"-"`
}

// DataDir is Runwatch's folder: RUNWATCH_DATA_DIR when set, else the OS's
// per-user config folder + "Runwatch". It is not created here.
func DataDir() (string, error) {
	if dir := os.Getenv(EnvDataDir); dir != "" {
		return dir, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find the data folder: %w", err)
	}
	return filepath.Join(base, "Runwatch"), nil
}

// Load reads config.json from the data folder (a missing file means the
// defaults, and is written so there is something to edit) and applies the
// environment overrides.
func Load() (Config, error) {
	dir, err := DataDir()
	if err != nil {
		return Config{}, err
	}
	c := Config{Roots: []string{}, Addr: DefaultAddr, Managed: map[string]string{}, Services: true, Notifications: true, DataDir: dir}
	path := filepath.Join(dir, "config.json")
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return c, fmt.Errorf("create the data folder: %w", err)
		}
		out, _ := json.MarshalIndent(c, "", "  ") // a struct of strings and maps always encodes
		if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
			return c, fmt.Errorf("write %s: %w", path, err)
		}
	case err != nil:
		return c, fmt.Errorf("read %s: %w", path, err)
	default:
		if err := json.Unmarshal(raw, &c); err != nil {
			return c, fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
	}
	if err := c.applyEnv(); err != nil {
		return c, err
	}
	if c.Addr == "" {
		c.Addr = DefaultAddr
	}
	return c, nil
}

// applyEnv applies the RUNWATCH_* overrides.
func (c *Config) applyEnv() error {
	if v := os.Getenv(EnvRoots); v != "" {
		c.Roots = filepath.SplitList(v)
	}
	if v := os.Getenv(EnvAddr); v != "" {
		c.Addr = v
	}
	if v := os.Getenv(EnvAPI); v != "" {
		c.GitHubAPI = v
	}
	c.Token = strings.TrimSpace(os.Getenv(EnvToken))
	switch v := os.Getenv(EnvServices); v {
	case "":
	case "off":
		c.Services = false
	case "on":
		c.Services = true
	default:
		return fmt.Errorf("%s=%q: want on or off", EnvServices, v)
	}
	if v := os.Getenv(EnvPollMS); v != "" {
		ms, err := strconv.Atoi(v)
		if err != nil || ms < 50 {
			return fmt.Errorf("%s=%q: want milliseconds, 50 or more", EnvPollMS, v)
		}
		c.Poll = time.Duration(ms) * time.Millisecond
	}
	return nil
}
