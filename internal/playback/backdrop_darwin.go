//go:build darwin

package playback

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
)

// Rect uses MyGo logical points with its origin at the content's top-left.
type Rect struct{ X, Y, Width, Height float64 }

// Surface describes one committed region of the MyGo interface.
type Surface struct {
	Rect    Rect
	Radius  float64
	Visible bool
}

// Materials places window-local blur below foreground MyGo controls. The
// interface should leave these regions translucent. With ReduceTransparency,
// draw opaque UI surfaces; native blur is disabled to honor that preference.
type Materials struct {
	Sidebar, Card, Playback, Toggle Surface
	Dark                            bool
	ReduceTransparency              bool
}

type materialFrame struct {
	rect        rectangle
	radius      float64
	initialized bool
}

func (c *Controller) createBackdrop(surface objc.ID) error {
	c.posterLayer = send(send(class("CALayer"), "alloc"), "init")
	c.posterHost = nativeAPI.initRect(send(class("NSView"), "alloc"), objc.RegisterName("initWithFrame:"), rectangle{})
	if c.posterLayer == 0 || c.posterHost == 0 {
		return errors.New("create native backdrop host")
	}
	send(c.posterLayer, "setContentsGravity:", nsString("resizeAspectFill"))
	send(c.posterLayer, "setMasksToBounds:", true)
	send(c.posterHost, "setLayer:", c.posterLayer)
	send(c.posterHost, "setWantsLayer:", true)
	send(c.posterHost, "setHidden:", true)
	send(c.content, "addSubview:positioned:relativeTo:", c.posterHost, int64(-1), c.host)
	for index := range c.effects {
		effect := nativeAPI.initRect(send(class("NSVisualEffectView"), "alloc"), objc.RegisterName("initWithFrame:"), rectangle{})
		if effect == 0 {
			return errors.New("create window-local blur material")
		}
		c.effects[index] = effect
		// WithinWindow samples the actual sibling movie/image, not the desktop.
		send(effect, "setBlendingMode:", int64(1))
		send(effect, "setState:", int64(1))
		send(effect, "setMaterial:", int64(13)) // NSVisualEffectMaterialHUDWindow
		send(effect, "setWantsLayer:", true)
		send(send(effect, "layer"), "setMasksToBounds:", true)
		send(effect, "setHidden:", true)
		send(c.content, "addSubview:positioned:relativeTo:", effect, int64(-1), surface)
	}
	return nil
}

// SetVisible controls the entire native backdrop and its material regions.
// Settings can hide all backgrounds while SetPageVisible independently decides
// whether the Endfield movie may play.
func (c *Controller) SetVisible(visible bool) {
	mygo.RunOnMain(func() {
		if !c.closed {
			c.backgroundVisible = visible
			c.refresh()
		}
	})
}

// SetMaterials accepts committed control bounds. Calls with unchanged values
// do nothing, so a layout observer may invoke this throughout an animation.
func (c *Controller) SetMaterials(materials Materials) {
	mygo.RunOnMain(func() {
		if c.closed || materials == c.materials {
			return
		}
		c.materials = materials
		c.layout()
		c.refreshMaterials()
	})
}

func (c *Controller) layoutMaterials(contentHeight float64) {
	surfaces := [4]Surface{c.materials.Sidebar, c.materials.Card, c.materials.Playback, c.materials.Toggle}
	if !c.materialAppearanceSet || c.materialDark != c.materials.Dark {
		appearance := "NSAppearanceNameVibrantLight"
		if c.materials.Dark {
			appearance = "NSAppearanceNameVibrantDark"
		}
		nativeAppearance := send(class("NSAppearance"), "appearanceNamed:", nsString(appearance))
		for _, effect := range c.effects {
			send(effect, "setAppearance:", nativeAppearance)
		}
		c.materialDark, c.materialAppearanceSet = c.materials.Dark, true
	}
	for index, surface := range surfaces {
		effect := c.effects[index]
		if effect == 0 {
			continue
		}
		r := surface.Rect
		if !finite(r.X, r.Y, r.Width, r.Height, surface.Radius) {
			send(effect, "setHidden:", true)
			c.materialVisible[index] = false
			continue
		}
		frame := rectangle{X: r.X, Y: contentHeight - r.Y - r.Height, Width: max(0, r.Width), Height: max(0, r.Height)}
		cached := c.materialFrames[index]
		if !cached.initialized || frame != cached.rect {
			nativeAPI.setRect(effect, objc.RegisterName("setFrame:"), frame)
		}
		radius := max(0, surface.Radius)
		if !cached.initialized || radius != cached.radius {
			send(send(effect, "layer"), "setCornerRadius:", radius)
		}
		c.materialFrames[index] = materialFrame{rect: frame, radius: radius, initialized: true}
	}
}

func finite(values ...float64) bool {
	for _, number := range values {
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return false
		}
	}
	return true
}

func (c *Controller) refreshMaterials() {
	for index, surface := range [4]Surface{c.materials.Sidebar, c.materials.Card, c.materials.Playback, c.materials.Toggle} {
		visible := c.backgroundVisible && surface.Visible && surface.Rect.Width > 0 && surface.Rect.Height > 0 && finite(surface.Rect.X, surface.Rect.Y, surface.Rect.Width, surface.Rect.Height, surface.Radius) && !c.materials.ReduceTransparency
		if visible != c.materialVisible[index] {
			send(c.effects[index], "setHidden:", !visible)
			c.materialVisible[index] = visible
		}
	}
}

func (c *Controller) closeBackdrop() {
	for _, effect := range c.effects {
		send(effect, "removeFromSuperview")
		release(effect)
	}
	c.effects = [4]objc.ID{}
	send(c.posterLayer, "setContents:", objc.ID(0))
	if c.posterImage != 0 {
		imageAPI.release(uintptr(c.posterImage))
		c.posterImage = 0
	}
	for _, image := range c.bankImages {
		if image != 0 {
			imageAPI.release(uintptr(image))
		}
	}
	c.bankImages = [2]objc.ID{}
	send(c.posterHost, "removeFromSuperview")
	release(c.posterHost)
	release(c.posterLayer)
	c.posterHost, c.posterLayer = 0, 0
}

var imageAPI struct {
	once                 sync.Once
	err                  error
	data                 func(uintptr, *byte, int64) uintptr
	source               func(uintptr, uintptr) uintptr
	image                func(uintptr, uint64, uintptr) uintptr
	dictionary           func(uintptr, *uintptr, *uintptr, int64, uintptr, uintptr) uintptr
	release              func(uintptr)
	retain               func(uintptr) uintptr
	cacheNow, cache, yes uintptr
}

func loadImages() error {
	imageAPI.once.Do(func() {
		for _, framework := range []string{"CoreFoundation", "ImageIO"} {
			if _, err := purego.Dlopen("/System/Library/Frameworks/"+framework+".framework/"+framework, purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
				imageAPI.err = err
				return
			}
		}
		for _, function := range []struct {
			name   string
			target any
		}{
			{"CFDataCreate", &imageAPI.data}, {"CGImageSourceCreateWithData", &imageAPI.source}, {"CGImageSourceCreateImageAtIndex", &imageAPI.image}, {"CFDictionaryCreate", &imageAPI.dictionary}, {"CFRelease", &imageAPI.release}, {"CFRetain", &imageAPI.retain},
		} {
			address, err := purego.Dlsym(purego.RTLD_DEFAULT, function.name)
			if err != nil {
				imageAPI.err = err
				return
			}
			purego.RegisterFunc(function.target, address)
		}
		for _, constant := range []struct {
			name   string
			target *uintptr
		}{
			{"kCGImageSourceShouldCacheImmediately", &imageAPI.cacheNow}, {"kCGImageSourceShouldCache", &imageAPI.cache}, {"kCFBooleanTrue", &imageAPI.yes},
		} {
			address, err := purego.Dlsym(purego.RTLD_DEFAULT, constant.name)
			if err != nil {
				imageAPI.err = err
				return
			}
			*constant.target = **(**uintptr)(unsafe.Pointer(&address))
		}
	})
	return imageAPI.err
}

// SetBackdrop reads and decodes a local PNG/WebP/other ImageIO-supported image.
// Call it on a worker; decoding completes before the small main-thread publish.
func (c *Controller) SetBackdrop(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("backdrop requires an absolute local path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read native backdrop: %w", err)
	}
	return c.SetBackdropData(data)
}

// SetBackdropData decodes an immutable poster without exposing its pixels to
// the UI renderer. Empty data removes the current poster.
func (c *Controller) SetBackdropData(data []byte) error {
	image, err := decodeBackdrop(data)
	if err != nil {
		return err
	}
	published := false
	mygo.RunOnMain(func() {
		if c.closed {
			err = errors.New("backdrop controller is closed")
			return
		}
		c.backdropKey = ""
		c.publishImage(objc.ID(image))
		published = true
		c.refresh()
	})
	if !published && image != 0 {
		imageAPI.release(image)
	}
	if !published && err == nil {
		return errors.New("backdrop requires an active event loop")
	}
	return err
}

// decodeBackdrop returns one owned CGImage reference; it has no UI access.
func decodeBackdrop(data []byte) (uintptr, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if err := loadImages(); err != nil {
		return 0, err
	}
	cfData := imageAPI.data(0, &data[0], int64(len(data)))
	runtime.KeepAlive(data)
	if cfData == 0 {
		return 0, errors.New("allocate native backdrop data")
	}
	defer imageAPI.release(cfData)
	source := imageAPI.source(cfData, 0)
	if source == 0 {
		return 0, errors.New("ImageIO cannot read this backdrop image")
	}
	defer imageAPI.release(source)
	keys := [2]uintptr{imageAPI.cacheNow, imageAPI.cache}
	values := [2]uintptr{imageAPI.yes, imageAPI.yes}
	options := imageAPI.dictionary(0, &keys[0], &values[0], 2, 0, 0)
	if options == 0 {
		return 0, errors.New("create ImageIO decode options")
	}
	image := imageAPI.image(source, 0, options)
	imageAPI.release(options)
	if image == 0 {
		return 0, errors.New("ImageIO cannot decode this backdrop image")
	}
	return image, nil
}

// publishImage transfers one owned reference to the currently shown poster.
// Layer contents retain their own additional reference. Main thread only.
func (c *Controller) publishImage(image objc.ID) {
	send(class("CATransaction"), "begin")
	send(class("CATransaction"), "setDisableActions:", true)
	send(c.posterLayer, "setContents:", image)
	send(class("CATransaction"), "commit")
	old := c.posterImage
	c.posterImage = image
	if old != 0 {
		imageAPI.release(uintptr(old))
	}
}

// LoadBackdrops prepares both game posters on a worker and publishes the pair
// atomically. Selection always uses the latest key, including a page change
// that happened while ImageIO was decoding the pair.
func (c *Controller) LoadBackdrops(arknightsPath, endfieldPath string) error {
	if !filepath.IsAbs(arknightsPath) || !filepath.IsAbs(endfieldPath) {
		return errors.New("backdrop bank requires absolute local paths")
	}
	arknights, err := os.ReadFile(arknightsPath)
	if err != nil {
		return fmt.Errorf("read Arknights backdrop: %w", err)
	}
	endfield, err := os.ReadFile(endfieldPath)
	if err != nil {
		return fmt.Errorf("read Endfield backdrop: %w", err)
	}
	return c.LoadBackdropData(arknights, endfield)
}

// LoadBackdropData is the embedded-data counterpart of LoadBackdrops. Identical
// image bytes reuse the bank and never repeat ImageIO decoding. Calls belong on
// a worker, never in a paint callback; SelectBackdrop performs the UI-only step.
func (c *Controller) LoadBackdropData(arknights, endfield []byte) error {
	if len(arknights) == 0 || len(endfield) == 0 {
		return errors.New("both backdrop bank images are required")
	}
	c.bankLoadMu.Lock()
	defer c.bankLoadMu.Unlock()
	hashes := [2][32]byte{sha256.Sum256(arknights), sha256.Sum256(endfield)}
	if c.bankLoaded && hashes == c.bankHashes {
		return nil
	}
	first, err := decodeBackdrop(arknights)
	if err != nil {
		return fmt.Errorf("decode Arknights backdrop: %w", err)
	}
	second, err := decodeBackdrop(endfield)
	if err != nil {
		imageAPI.release(first)
		return fmt.Errorf("decode Endfield backdrop: %w", err)
	}
	published := false
	mygo.RunOnMain(func() {
		if c.closed {
			err = errors.New("backdrop controller is closed")
			return
		}
		old := c.bankImages
		c.bankImages = [2]objc.ID{objc.ID(first), objc.ID(second)}
		c.selectBank()
		for _, image := range old {
			if image != 0 {
				imageAPI.release(uintptr(image))
			}
		}
		published = true
		c.refresh()
	})
	if !published {
		imageAPI.release(first)
		imageAPI.release(second)
		if err == nil {
			err = errors.New("backdrop bank requires an active event loop")
		}
		return err
	}
	c.bankHashes, c.bankLoaded = hashes, true
	return nil
}

// SelectBackdrop changes the poster synchronously without I/O or decoding.
// A valid key may be selected before loading; BackdropReady remains false and
// the pending key is applied automatically when LoadBackdropData publishes.
func (c *Controller) SelectBackdrop(key string) error {
	if key != "arknights" && key != "endfield" {
		return fmt.Errorf("unknown backdrop key %q", key)
	}
	var err error
	mygo.RunOnMain(func() {
		if c.closed {
			err = errors.New("backdrop controller is closed")
			return
		}
		c.backdropKey = key
		c.selectBank()
		c.refresh()
	})
	return err
}

func (c *Controller) selectBank() {
	index := 0
	if c.backdropKey == "endfield" {
		index = 1
	} else if c.backdropKey != "arknights" {
		return
	}
	image := c.bankImages[index]
	if image == c.posterImage {
		return
	}
	if image != 0 {
		imageAPI.retain(uintptr(image))
	}
	c.publishImage(image)
}
