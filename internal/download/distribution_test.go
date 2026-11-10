package download

import (
	"encoding/json"
	"maps"
	"testing"
)

func TestDistributionRequests(t *testing.T) {
	for _, tc := range []struct {
		game                                          GameID
		appcode, launcherAppcode, channel, subChannel string
	}{
		{Arknights, "GzD1CpaWgmSq1wew", "abYeZZ16BPluCFyT", "1", "1"},
		{Endfield, "6LL0KJuqHBVz33WK", "abYeZZ16BPluCFyT", "1", "1"},
	} {
		t.Run(string(tc.game), func(t *testing.T) {
			d, err := distributionFor(tc.game)
			if err != nil {
				t.Fatal(err)
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
