package app

import "github.com/egoist/mygo/ui"

const (
	fontSmall     = 12
	fontBody      = 14
	fontTitle     = 20
	controlRadius = 4
	panelInset    = 12
	panelRadius   = controlRadius + panelInset
	hairline      = .5
)

// Radix Colors sRGB scales. Roles follow steps 1–2 for surfaces, 3–5 for
// controls, 6–8 for borders, 9–10 for solid actions and 11–12 for text.
// Source and MIT attribution: resources/licenses/third-party.md.
var sandLight = [...]ui.Color{
	ui.Hex("#fdfdfc"), ui.Hex("#f9f9f8"), ui.Hex("#f1f0ef"), ui.Hex("#e9e8e6"),
	ui.Hex("#e2e1de"), ui.Hex("#dad9d6"), ui.Hex("#cfceca"), ui.Hex("#bcbbb5"),
	ui.Hex("#8d8d86"), ui.Hex("#82827c"), ui.Hex("#63635e"), ui.Hex("#21201c"),
}

var sandDark = [...]ui.Color{
	ui.Hex("#111110"), ui.Hex("#191918"), ui.Hex("#222221"), ui.Hex("#2a2a28"),
	ui.Hex("#31312e"), ui.Hex("#3b3a37"), ui.Hex("#494844"), ui.Hex("#62605b"),
	ui.Hex("#6f6d66"), ui.Hex("#7c7b74"), ui.Hex("#b5b3ad"), ui.Hex("#eeeeec"),
}

type palette struct {
	ui.Theme
	panel, divider, borderHover ui.Color
}

func radixPalette(base ui.Theme) palette {
	sand := sandLight
	amber9, amber10 := ui.Hex("#ffc53d"), ui.Hex("#ffba18")
	red11, green11 := ui.Hex("#ce2c31"), ui.Hex("#218358")
	amber11 := ui.Hex("#ab6400")
	if base.Dark {
		sand = sandDark
		amber10 = ui.Hex("#ffd60a")
		red11, green11 = ui.Hex("#ff9592"), ui.Hex("#3dd68c")
		amber11 = ui.Hex("#ffca16")
	}
	base.Background = sand[0]
	base.Surface, base.SurfaceHover, base.SurfacePressed = sand[2], sand[3], sand[4]
	base.Border = sand[6]
	base.Text, base.TextMuted = sand[11], sand[10]
	base.Accent, base.AccentHover, base.AccentPressed = amber9, amber10, amber10
	base.AccentText = ui.Hex("#4f3422") // Amber 12 light, including on dark-mode Amber 9/10.
	base.Danger, base.Warning, base.Success = red11, amber11, green11
	base.Selection, base.Focus = sand[4], sand[7]
	base.Inverse, base.InverseText = sand[1], sand[11]
	base.Scrollbar = sand[8]
	base.FontSize, base.Radius, base.Spacing = fontBody, controlRadius, 4
	return palette{Theme: base, panel: sand[1], divider: sand[5], borderHover: sand[7]}
}

// Game actions use the accent from each official website; all surrounding
// surfaces and text retain the Radix roles. Sources: resources/licenses/third-party.md.
func (a *App) palette(base ui.Theme) palette {
	colors := radixPalette(base)
	switch a.page {
	case arknights:
		colors.Accent = ui.Hex("#18d1ff")
	case endfield:
		colors.Accent = ui.Hex("#fffa00")
	case settings:
		colors.Accent, colors.AccentHover, colors.AccentPressed = sandLight[0], sandLight[2], sandLight[3]
		colors.AccentText = sandLight[11]
		return colors
	default:
		return colors
	}
	colors.AccentText = sandLight[11]
	colors.AccentHover = colors.Accent.Mix(sandLight[0], .08)
	colors.AccentPressed = colors.Accent.Mix(sandLight[11], .08)
	return colors
}

// TooltipBase retains native focus and accessibility behavior without the
// styled widget's extra font size, drop shadow or non-palette colors.
func tooltip(c *ui.Context, anchor ui.Element, text string) {
	if text == "" {
		return
	}
	anchor.Description(text)
	t := c.Theme()
	ui.TooltipBase(c, anchor, func(tip ui.Element) {
		tip.MaxWidth(360).Padding(8, 12).Radius(controlRadius).
			Background(t.Inverse).TextColor(t.InverseText).Border(hairline, t.Border).
			AttachTo(anchor, ui.AnchorTop, ui.AnchorBottom).Margin(0, 0, 6, 0).
			Children(func() { ui.Text(c, text).FontSize(fontSmall) })
	})
}
