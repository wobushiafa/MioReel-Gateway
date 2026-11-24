package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigWithDurationStringAndOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "gateway.json")
	content := `{
		"listen_addr": "127.0.0.1:9000",
		"auth_tokens": ["tok-1"],
		"profile_refresh_interval": "15m"
	}`
	if err := os.WriteFile(cfgFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9000" {
		t.Fatalf("ListenAddr got=%q want=127.0.0.1:9000", cfg.ListenAddr)
	}
	if cfg.ProfileRefreshInterval != 15*time.Minute {
		t.Fatalf("ProfileRefreshInterval got=%v want=15m", cfg.ProfileRefreshInterval)
	}

	t.Setenv("MIOREEL_LISTEN_ADDR", "0.0.0.0:9100")
	cfgEnv, err := Load(cfgFile)
	if err != nil {
		t.Fatalf("Load with env failed: %v", err)
	}
	if cfgEnv.ListenAddr != "0.0.0.0:9100" {
		t.Fatalf("env override ListenAddr got=%q want=0.0.0.0:9100", cfgEnv.ListenAddr)
	}
}

func TestResolveListenAddr(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		listen  string
		port    int
		want    string
		wantErr bool
	}{
		{name: "config only", base: "127.0.0.1:18662", want: "127.0.0.1:18662"},
		{name: "listen flag full", base: ":18662", listen: "0.0.0.0:9090", want: "0.0.0.0:9090"},
		{name: "listen flag bare port", base: ":18662", listen: "9090", want: ":9090"},
		{name: "port flag preserves host", base: "127.0.0.1:18662", port: 9091, want: "127.0.0.1:9091"},
		{name: "port flag on :18662", base: ":18662", port: 9092, want: ":9092"},
		{name: "invalid port", base: ":18662", port: 70000, wantErr: true},
		{name: "invalid addr", base: "badhost", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveListenAddr(tc.base, tc.listen, tc.port)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got=%q want=%q", got, tc.want)
			}
		})
	}
}
