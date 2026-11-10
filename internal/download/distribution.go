package download

import (
	"encoding/json"
	"fmt"
)

// GameID identifies an independently installed distribution. Its value is
// persisted in records and used as the download directory name.
type GameID string

const (
	Arknights GameID = "arknights"
	Endfield  GameID = "endfield"
)

type distribution struct {
	appcode, launcherAppcode, channel, subChannel string
	differential                                  bool
}

func distributionFor(game GameID) (distribution, error) {
	switch game {
	case Arknights:
		return distribution{"GzD1CpaWgmSq1wew", "abYeZZ16BPluCFyT", "1", "1", true}, nil
	case Endfield:
		return distribution{"6LL0KJuqHBVz33WK", "abYeZZ16BPluCFyT", "1", "1", true}, nil
	default:
		return distribution{}, fmt.Errorf("unknown game ID %q", game)
	}
}

// latestGameBody asks for the full package when version is empty, or for a
// patch from version otherwise.
func (d distribution) latestGameBody(version string) ([]byte, error) {
	return json.Marshal(map[string]any{"seq": "5", "proxy_reqs": []any{map[string]any{
		"kind": "get_latest_game", "get_latest_game_req": map[string]string{
			"appcode": d.appcode, "launcher_appcode": d.launcherAppcode,
			"channel": d.channel, "sub_channel": d.subChannel, "version": version,
		},
	}}})
}
