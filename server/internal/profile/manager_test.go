package profile

import (
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"mioreel-gateway/server/internal/config"
	"mioreel-gateway/server/internal/observability"
)

func TestProfileManagerFallbackAndValidation(t *testing.T) {
	metrics := observability.NewMetrics()
	cfg := config.Default()

	// Invalid env material should retain built-in fallback and mark degraded
	cfg.ProfileID = "NjQ2NjM3Nzk0"
	cfg.ProfileMaterial = base64.StdEncoding.EncodeToString([]byte("too-short"))
	mgr := NewManager(cfg, slog.New(slog.DiscardHandler), metrics)

	id, sdk, _, degraded := mgr.Status()
	if id != "646637794" || sdk != "v04.10.03-ml-android" {
		t.Fatalf("unexpected fallback profile id=%s sdk=%s", id, sdk)
	}
	if !degraded {
		t.Fatal("expected degraded=true after invalid external profile")
	}

	// Valid file profile should rotate cleanly
	dir := t.TempDir()
	profPath := filepath.Join(dir, "profile.json")
	newMat := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	newID := base64.StdEncoding.EncodeToString([]byte("999888777"))
	if err := os.WriteFile(profPath, []byte(`{"id":"`+newID+`","material":"`+newMat+`","sdkVersion":"v04.10.03-ml-android"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg2 := config.Default()
	cfg2.ProfileFile = profPath
	mgr2 := NewManager(cfg2, slog.New(slog.DiscardHandler), metrics)
	id2, _, _, degraded2 := mgr2.Status()
	if id2 != "999888777" || degraded2 {
		t.Fatalf("expected rotated profile 999888777 degraded=false, got id=%s degraded=%v", id2, degraded2)
	}

	// Also support profile_id / profile_material / profile_sdk_version schema (docs/08-configuration.md §5.2)
	profPath3 := filepath.Join(dir, "profile_v2.json")
	newID3 := base64.StdEncoding.EncodeToString([]byte("111222333"))
	if err := os.WriteFile(profPath3, []byte(`{"profile_id":"`+newID3+`","profile_material":"`+newMat+`","profile_sdk_version":"v04.10.03-ml-android"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg3 := config.Default()
	cfg3.ProfileFile = profPath3
	mgr3 := NewManager(cfg3, slog.New(slog.DiscardHandler), metrics)
	id3, _, _, degraded3 := mgr3.Status()
	if id3 != "111222333" || degraded3 {
		t.Fatalf("expected rotated profile 111222333 degraded=false, got id=%s degraded=%v", id3, degraded3)
	}
}
