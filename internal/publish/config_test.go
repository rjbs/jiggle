package publish

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/rjbs/jiggle/internal/store"
)

// configIs checks what LoadConfig makes of a database with the given
// settings, and which ways it says the site is published.
func configIs(t *testing.T, settings map[string]string, want Config, targets ...string) {
	t.Helper()
	s, err := store.Create(filepath.Join(t.TempDir(), "jiggle.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for name, value := range settings {
		if _, err := s.DB.Exec(`INSERT INTO setting (name, value) VALUES (?, ?)`, name, value); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadConfig(context.Background(), s.DB)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("settings %v:\n got: %+v\nwant: %+v", settings, got, want)
	}
	if !reflect.DeepEqual(got.Targets(), targets) {
		t.Errorf("settings %v: targets %v, want %v", settings, got.Targets(), targets)
	}
}

func TestLoadConfig(t *testing.T) {
	configIs(t, map[string]string{"title": "Photos"}, Config{RsyncCommand: "rsync"})

	configIs(t, map[string]string{SettingRsyncTarget: "host:/www/", SettingRsyncCommand: "/opt/bin/rsync"},
		Config{RsyncTarget: "host:/www/", RsyncCommand: "/opt/bin/rsync"}, "rsync")

	configIs(t,
		map[string]string{
			SettingBunnyZone: "photos", SettingBunnyStorageURL: "https://ny.storage.example.com",
			SettingBunnyPullZoneID: "12345", SettingBunnyPurgeHosts: "a.example.com  b.example.com",
			SettingRsyncTarget: "host:/www/",
		},
		Config{
			RsyncTarget: "host:/www/", RsyncCommand: "rsync",
			BunnyZone: "photos", BunnyStorageURL: "https://ny.storage.example.com", BunnyPullZoneID: "12345",
			BunnyPurgeHosts: []string{"a.example.com", "b.example.com"},
		},
		"rsync", "bunny")
}
