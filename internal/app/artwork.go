package app

import (
	_ "embed"
	"fmt"
	"os"

	"github.com/egoist/mygo/ui"
)

// Official launcher artwork. See resources/licenses/third-party.md for provenance.
//
//go:embed artwork/arknights-background.png
var arknightsBackground []byte

//go:embed artwork/endfield-background.webp
var endfieldBackground []byte

// Artwork holds immutable bitmaps decoded outside the UI thread.
type Artwork struct {
	arknights *ui.Bitmap
	endfield  *ui.Bitmap
}

// BuiltinBackgrounds returns embedded image bytes for native decoding.
// Callers must treat the returned data as read-only.
func BuiltinBackgrounds() (arknights, endfield []byte) {
	return arknightsBackground, endfieldBackground
}

// LoadArtworkFiles decodes a verified official cache snapshot on a worker.
func LoadArtworkFiles(arknightsPath, endfieldPath string) (*Artwork, error) {
	load := func(path string) (*ui.Bitmap, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return ui.DecodeBitmap(data)
	}
	arknights, err := load(arknightsPath)
	if err != nil {
		return nil, fmt.Errorf("decode cached Arknights background: %w", err)
	}
	endfield, err := load(endfieldPath)
	if err != nil {
		return nil, fmt.Errorf("decode cached Endfield background: %w", err)
	}
	return &Artwork{arknights: arknights, endfield: endfield}, nil
}

func LoadArtwork() (*Artwork, error) {
	arknights, err := ui.DecodeBitmap(arknightsBackground)
	if err != nil {
		return nil, fmt.Errorf("decode Arknights launcher background: %w", err)
	}
	endfield, err := ui.DecodeBitmap(endfieldBackground)
	if err != nil {
		return nil, fmt.Errorf("decode Endfield launcher background: %w", err)
	}
	return &Artwork{arknights: arknights, endfield: endfield}, nil
}

func (a *Artwork) background(p page) *ui.Bitmap {
	if p == arknights {
		return a.arknights
	}
	return a.endfield
}
