package app

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// GameUpdateState keeps a network check separate from the usable installation.
type GameUpdateState struct {
	Stage, InstalledVersion, LatestVersion, Error string
	ActionError                                   string
}

func (a *App) SetGameUpdate(game string, state GameUpdateState) {
	a.gameUpdates[game] = state
}

func (a *App) gameUpdateDetails(c *ui.Context) {
	game := a.selectedGame()
	s := a.gameUpdates[game]
	t := c.Theme()
	if s.Stage == "available" {
		ui.Text(c, fmt.Sprintf("发现游戏更新 %s", s.LatestVersion)).FontSize(fontBody)
	}
	if s.Error != "" {
		failure := ui.Text(c, "检查游戏更新失败，将自动重试").FontSize(fontSmall).TextColor(t.Danger)
		tooltip(c, failure, s.Error)
	}
	if s.ActionError != "" {
		ui.Text(c, s.ActionError).FontSize(fontSmall).TextColor(t.Danger)
	}
}
