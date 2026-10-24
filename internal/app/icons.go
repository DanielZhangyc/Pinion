package app

import (
	_ "embed"

	"github.com/egoist/mygo/ui"
)

//go:embed icons/arknights.svg
var arknightsSVG []byte

//go:embed icons/endfield.svg
var endfieldSVG []byte

//go:embed icons/settings.svg
var settingsSVG []byte

//go:embed icons/sidebar.svg
var sidebarSVG []byte

//go:embed icons/pause.svg
var pauseSVG []byte

//go:embed icons/play.svg
var playSVG []byte

var (
	arknightsIcon = ui.MustParseSVG(arknightsSVG)
	endfieldIcon  = ui.MustParseSVG(endfieldSVG)
	settingsIcon  = ui.MustParseSVG(settingsSVG)
	sidebarIcon   = ui.MustParseSVG(sidebarSVG)
	pauseIcon     = ui.MustParseSVG(pauseSVG)
	playIcon      = ui.MustParseSVG(playSVG)
)
