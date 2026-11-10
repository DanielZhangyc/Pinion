package download

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"
)

func TestDistributionRequests(t *testing.T) {
	for _, tc := range []struct {
		game                                          GameID
		appcode, launcherAppcode, channel, subChannel string
		differential                                  bool
	}{
		{Arknights, "GzD1CpaWgmSq1wew", "abYeZZ16BPluCFyT", "1", "1", true},
		{ArknightsBilibili, "GzD1CpaWgmSq1wew", "Ae1Pa3Oi2PRHpLAP", "2", "2", false},
		{Endfield, "6LL0KJuqHBVz33WK", "abYeZZ16BPluCFyT", "1", "1", true},
	} {
		t.Run(string(tc.game), func(t *testing.T) {
			d, err := distributionFor(tc.game)
			if err != nil {
				t.Fatal(err)
			}
			if d.differential != tc.differential {
				t.Fatalf("differential = %v, want %v", d.differential, tc.differential)
			}
			for _, version := range []string{"", "78.0.0"} {
				body, err := d.latestGameBody(version)
				if err != nil {
					t.Fatal(err)
				}
				var request struct {
					Seq       string `json:"seq"`
					ProxyReqs []struct {
						Kind string            `json:"kind"`
						Req  map[string]string `json:"get_latest_game_req"`
					} `json:"proxy_reqs"`
				}
				if err := json.Unmarshal(body, &request); err != nil {
					t.Fatal(err)
				}
				if request.Seq != "5" || len(request.ProxyReqs) != 1 || request.ProxyReqs[0].Kind != "get_latest_game" {
					t.Fatalf("unexpected request envelope: %s", body)
				}
				want := map[string]string{
					"appcode": tc.appcode, "launcher_appcode": tc.launcherAppcode,
					"channel": tc.channel, "sub_channel": tc.subChannel, "version": version,
				}
				if got := request.ProxyReqs[0].Req; !maps.Equal(got, want) {
					t.Fatalf("request = %v, want %v", got, want)
				}
			}
		})
	}
}

func TestUnknownDistributionRejected(t *testing.T) {
	for _, game := range []GameID{"", "Arknights", "arknights-unknown"} {
		if _, err := distributionFor(game); err == nil {
			t.Fatalf("accepted unknown game ID %q", game)
		}
		if root, err := New(t.TempDir()).openGame(game, true); err == nil {
			root.Close()
			t.Fatalf("created directory for unknown game ID %q", game)
		}
	}
}

// A zero network budget proves the refusal happens before any official request.
func TestBilibiliDifferentialDisabled(t *testing.T) {
	m := New(t.TempDir())
	m.client.Transport = &trafficTransport{base: m.client.Transport}
	if _, _, err := m.officialPatch(context.Background(), ArknightsBilibili, "78.0.0"); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("Bilibili patch request: %v", err)
	}
	if _, err := m.resolveDifferential(context.Background(), ArknightsBilibili, "78.0.0", ""); err == nil || !strings.Contains(err.Error(), "not enabled") {
		t.Fatalf("Bilibili differential plan: %v", err)
	}
}

func TestDistributionDirectoriesIsolated(t *testing.T) {
	m := New(t.TempDir())
	games := []GameID{Arknights, ArknightsBilibili, Endfield}
	markers := map[GameID][]byte{}
	for _, game := range games {
		root, err := m.openGame(game, true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasSuffix(root.Name(), "/"+string(game)) {
			root.Close()
			t.Fatalf("%s directory = %s", game, root.Name())
		}
		markers[game], err = root.ReadFile(".gryph-download.json")
		root.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	for i, a := range games {
		for _, b := range games[i+1:] {
			if string(markers[a]) == string(markers[b]) {
				t.Fatalf("%s and %s share an ownership marker", a, b)
			}
		}
	}
	// Another distribution's marker must not let a directory be adopted.
	root, err := m.openGame(ArknightsBilibili, false)
	if err != nil {
		t.Fatal(err)
	}
	err = root.WriteFile(".gryph-download.json", markers[Arknights], 0644)
	root.Close()
	if err != nil {
		t.Fatal(err)
	}
	if root, err := m.openGame(ArknightsBilibili, true); err == nil || !strings.Contains(err.Error(), "marker mismatch") {
		if err == nil {
			root.Close()
		}
		t.Fatalf("adopted directory with official marker: %v", err)
	}
	if _, err := m.LoadRecord(ArknightsBilibili); err == nil || !strings.Contains(err.Error(), "marker mismatch") {
		t.Fatalf("read record from directory with official marker: %v", err)
	}
	root, err = m.openGame(Arknights, false)
	if err != nil {
		t.Fatalf("official directory affected: %v", err)
	}
	root.Close()
}
