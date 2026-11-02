package download

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestNumericGameVersions(t *testing.T) {
	// Pure comparison inputs: no installation records are written or adopted.
	for _, tc := range []struct {
		latest, installed string
		want              bool
	}{
		{"1.10", "1.9", true},
		{"1.9", "1.10", false},
		{"1.0.0.12", "1.0.0.9", true},
		{"1.0.0.9", "1.0.0.12", false},
		{"1.0", "1.0.0", false},
		{"1.0.1", "1.0", true},
		{"1.01", "1.1", false},
		{"1.100000000000000000000", "1.99999999999999999999", true},
	} {
		t.Run(tc.latest+"_vs_"+tc.installed, func(t *testing.T) {
			latest := VersionInfo{Version: tc.latest}
			if got := latest.UpdateAvailable(Record{Version: tc.installed}); got != tc.want {
				t.Fatalf("update available = %v, want %v", got, tc.want)
			}
		})
	}
}

// Query genuine official metadata without touching any game directory or
// fetching package contents. Canceled requests must remain errors.
func TestOfficialGameVersions(t *testing.T) {
	if os.Getenv("PINION_GAME_UPDATE_INTEGRATION") != "1" {
		t.Skip("set PINION_GAME_UPDATE_INTEGRATION=1 for official version queries")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	m := New("")
	defer m.client.CloseIdleConnections()
	for _, game := range []GameID{Arknights, Endfield} {
		t.Run(string(game), func(t *testing.T) {
			latest, err := m.LatestVersion(ctx, game)
			if err != nil {
				t.Fatal(err)
			}
			if !validVersion(latest.Version) || !validMD5(latest.ManifestMD5) {
				t.Fatal("official version identity is invalid")
			}
			identity := Record{Version: latest.Version, ManifestMD5: latest.ManifestMD5}
			if latest.UpdateAvailable(identity) {
				t.Fatal("identical official identity reported an update")
			}
			canceled, stop := context.WithCancel(ctx)
			stop()
			if _, err := m.LatestVersion(canceled, game); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled version check: %v", err)
			}
			t.Logf("official %s version=%s manifest=%s", game, latest.Version, latest.ManifestMD5)
		})
	}
}
