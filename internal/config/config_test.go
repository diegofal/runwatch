package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadWritesDefaultsThenReadsEdits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Runwatch") // does not exist yet
	t.Setenv(EnvDataDir, dir)
	for _, k := range []string{EnvRoots, EnvAddr, EnvAPI, EnvToken, EnvPollMS, EnvServices} {
		t.Setenv(k, "")
	}
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != DefaultAddr || !c.Notifications || !c.Services || len(c.Roots) != 0 || c.Token != "" || c.DataDir != dir {
		t.Fatalf("defaults = %+v", c)
	}
	path := filepath.Join(dir, "config.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the defaults must be written so there is a file to edit: %v", err)
	}
	for _, key := range []string{`"roots": []`, `"addr": "127.0.0.1:7878"`, `"managed": {}`, `"notifications": true`} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("config.json lacks %s:\n%s", key, raw)
		}
	}
	if strings.Contains(strings.ToLower(string(raw)), "token") {
		t.Errorf("a token must never be in the file:\n%s", raw)
	}

	edited := `{"roots":["D:\\runners"],"addr":"127.0.0.1:9000","managed":{"billing-*":"use the script"},"notifications":false,"githubApi":"https://ghe.example/api/v3"}`
	if err := os.WriteFile(path, []byte(edited), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err = Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Roots[0] != `D:\runners` || c.Addr != "127.0.0.1:9000" || c.Managed["billing-*"] != "use the script" || c.Notifications || c.GitHubAPI != "https://ghe.example/api/v3" {
		t.Errorf("edited = %+v", c)
	}
}

func TestEnvironmentWinsOverTheFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvDataDir, dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"roots":["a"],"addr":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvRoots, "x"+string(os.PathListSeparator)+"y")
	t.Setenv(EnvAddr, "127.0.0.1:0")
	t.Setenv(EnvAPI, "http://127.0.0.1:1234")
	t.Setenv(EnvToken, "  made-up-token\n")
	for _, bad := range []string{"fast", "10"} {
		t.Setenv(EnvPollMS, bad)
		if _, err := Load(); err == nil {
			t.Errorf("%s=%s must be refused", EnvPollMS, bad)
		}
	}
	t.Setenv(EnvPollMS, "250")
	t.Setenv(EnvServices, "maybe")
	if _, err := Load(); err == nil {
		t.Errorf("%s=maybe must be refused", EnvServices)
	}
	t.Setenv(EnvServices, "off")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Services {
		t.Error("RUNWATCH_SERVICES=off must turn service discovery off")
	}
	if c.Poll != 250*time.Millisecond {
		t.Errorf("Poll = %s", c.Poll)
	}
	if len(c.Roots) != 2 || c.Roots[1] != "y" || c.Addr != "127.0.0.1:0" || c.GitHubAPI != "http://127.0.0.1:1234" || c.Token != "made-up-token" {
		t.Errorf("config = %+v", c)
	}
	t.Setenv(EnvAddr, "")
	if c, _ = Load(); c.Addr != DefaultAddr {
		t.Errorf("an empty addr in the file falls back to the default, got %q", c.Addr)
	}
}

func TestLoadReportsABrokenFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvDataDir, dir)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"roots": [`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("err = %v; a broken config must stop the start, not silently become defaults", err)
	}
	file := filepath.Join(dir, "a-file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvDataDir, filepath.Join(file, "sub"))
	if _, err := Load(); err == nil {
		t.Error("a data folder that cannot be created must be an error")
	}
}

func TestDataDirDefault(t *testing.T) {
	t.Setenv(EnvDataDir, "")
	dir, err := DataDir()
	if err != nil || filepath.Base(dir) != "Runwatch" {
		t.Errorf("DataDir = %q, %v", dir, err)
	}
}
