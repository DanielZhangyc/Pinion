//go:build darwin

// Package playback composites a muted AVFoundation movie below MyGo's native
// surface. Video frames never enter Go memory or trigger a UI rebuild.
package playback

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"github.com/egoist/mygo"
)

// Status is a main-thread snapshot. ReadyForDisplay, rather than file existence,
// tells the interface when it can remove its static artwork.
type Status struct {
	Source          string
	Loaded          bool
	Playing         bool
	Visible         bool
	PageVisible     bool
	Paused          bool
	GameRunning     bool
	ReadyForDisplay bool
	BackdropReady   bool
	BackdropKey     string
	ItemStatus      int
	Rate            float32
	Reason          string
	Error           error
}

type observation struct{ center, token objc.ID }

// Controller's native objects and flags are confined to the main thread.
// Every public method can also be called by a worker while App.Run is active.
type Controller struct {
	window                                       *mygo.Window
	native, content, host, layer, player, looper objc.ID
	posterHost, posterLayer, posterImage         objc.ID
	bankImages                                   [2]objc.ID
	backdropKey                                  string
	bankLoadMu                                   sync.Mutex
	bankHashes                                   [2][32]byte
	bankLoaded                                   bool
	effects                                      [4]objc.ID
	materials                                    Materials
	materialFrames                               [4]materialFrame
	materialVisible                              [4]bool
	materialAppearanceSet, materialDark          bool
	backgroundBounds                             rectangle
	backgroundVisible                            bool
	observers                                    []observation
	off                                          []func()
	source                                       string
	page, paused, game, locked, sleeping         bool
	screensSleeping, sessionInactive             bool
	closed, playing                              bool
	inactiveSince                                time.Time
	failure                                      error
	errors                                       chan error
}

type rectangle struct{ X, Y, Width, Height float64 }

var nativeAPI struct {
	once      sync.Once
	err       error
	setRect   func(objc.ID, objc.SEL, rectangle)
	initRect  func(objc.ID, objc.SEL, rectangle) objc.ID
	session   func() uintptr
	dictGet   func(uintptr, uintptr) uintptr
	boolean   func(uintptr) bool
	cfString  func(uintptr, string, uint32) uintptr
	cfRelease func(uintptr)
	gravity   objc.ID
}

func loadNative() error {
	nativeAPI.once.Do(func() {
		frameworks := []string{"Foundation", "AppKit", "QuartzCore", "AVFoundation", "CoreGraphics", "CoreFoundation"}
		for _, name := range frameworks {
			if _, err := purego.Dlopen("/System/Library/Frameworks/"+name+".framework/"+name, purego.RTLD_NOW|purego.RTLD_GLOBAL); err != nil {
				nativeAPI.err = fmt.Errorf("load %s: %w", name, err)
				return
			}
		}
		lib, err := purego.Dlopen("/usr/lib/libobjc.A.dylib", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			nativeAPI.err = err
			return
		}
		address, err := purego.Dlsym(lib, "objc_msgSend")
		if err != nil {
			nativeAPI.err = err
			return
		}
		purego.RegisterFunc(&nativeAPI.setRect, address)
		purego.RegisterFunc(&nativeAPI.initRect, address)
		if objc.GetClass("AVPlayerLayer") == 0 || objc.GetClass("AVPlayerLooper") == 0 {
			nativeAPI.err = errors.New("AVFoundation player classes unavailable")
			return
		}
		gravityAddress, err := purego.Dlsym(purego.RTLD_DEFAULT, "AVLayerVideoGravityResizeAspectFill")
		if err != nil {
			nativeAPI.err = err
			return
		}
		// Dlsym gives the address of the exported NSString* variable. Keep
		// the address in pointer-sized storage while loading that variable,
		// matching MyGo's own exported-framework-constant bindings.
		nativeAPI.gravity = **(**objc.ID)(unsafe.Pointer(&gravityAddress))
		functions := []struct {
			name   string
			target any
		}{
			{"CGSessionCopyCurrentDictionary", &nativeAPI.session},
			{"CFDictionaryGetValue", &nativeAPI.dictGet},
			{"CFBooleanGetValue", &nativeAPI.boolean},
			{"CFStringCreateWithCString", &nativeAPI.cfString},
			{"CFRelease", &nativeAPI.cfRelease},
		}
		for _, f := range functions {
			address, err := purego.Dlsym(purego.RTLD_DEFAULT, f.name)
			if err != nil {
				nativeAPI.err = fmt.Errorf("resolve %s: %w", f.name, err)
				return
			}
			purego.RegisterFunc(f.target, address)
		}
	})
	return nativeAPI.err
}

func send(object objc.ID, selector string, args ...any) objc.ID {
	return object.Send(objc.RegisterName(selector), args...)
}
func value[T any](object objc.ID, selector string) T {
	return objc.Send[T](object, objc.RegisterName(selector))
}
func class(name string) objc.ID    { return objc.ID(objc.GetClass(name)) }
func nsString(text string) objc.ID { return send(class("NSString"), "stringWithUTF8String:", text) }
func release(object objc.ID) {
	if object != 0 {
		send(object, "release")
	}
}

// New attaches a hidden native layer. Call SetSource and SetPageVisible to play.
// Close is registered with Window.OnClosed and is idempotent.
func New(window *mygo.Window) (*Controller, error) {
	if window == nil {
		return nil, errors.New("playback requires a window")
	}
	var c *Controller
	var err error
	mygo.RunOnMain(func() {
		if err = loadNative(); err != nil {
			return
		}
		if window.IsDestroyed() {
			err = errors.New("playback window is destroyed")
			return
		}
		native := objc.ID(window.NativeHandle())
		content := send(native, "contentView")
		if native == 0 || content == 0 {
			err = errors.New("playback window has no native content view")
			return
		}
		send(content, "setWantsLayer:", true)
		parent := send(content, "layer")
		if parent == 0 {
			err = errors.New("playback content view has no layer")
			return
		}
		layer := send(send(class("AVPlayerLayer"), "alloc"), "init")
		if layer == 0 {
			err = errors.New("create AVPlayerLayer")
			return
		}
		c = &Controller{window: window, native: native, content: content, layer: layer, backdropKey: "endfield", backgroundVisible: true, errors: make(chan error, 1)}
		send(layer, "setVideoGravity:", nativeAPI.gravity)
		send(layer, "setMasksToBounds:", true)
		// AppKit owns the backing layer hierarchy. Give the movie its own
		// NSView instead of inserting an unmanaged sibling CALayer that AppKit
		// may hide or reorder when it commits its view tree.
		views := send(content, "subviews")
		var surface objc.ID
		for index := uint64(0); index < value[uint64](views, "count"); index++ {
			view := send(views, "objectAtIndex:", index)
			if objc.Send[bool](view, objc.RegisterName("isKindOfClass:"), class("MyGoSurfaceView")) {
				surface = view
				break
			}
		}
		if surface == 0 {
			release(layer)
			c = nil
			err = errors.New("MyGo native UI surface unavailable")
			return
		}
		c.host = nativeAPI.initRect(send(class("NSView"), "alloc"), objc.RegisterName("initWithFrame:"), rectangle{})
		if c.host == 0 {
			release(layer)
			c = nil
			err = errors.New("create video host view")
			return
		}
		send(c.host, "setLayer:", layer)
		send(c.host, "setWantsLayer:", true)
		// NSWindowBelow=-1: above the effect view, below MyGo's UI surface.
		send(content, "addSubview:positioned:relativeTo:", c.host, int64(-1), surface)
		if err = c.createBackdrop(surface); err != nil {
			c.close()
			c = nil
			return
		}
		c.layout()
		c.locked = sessionLocked()
		if err = c.observe(); err != nil {
			c.close()
			c = nil
			return
		}
		c.off = append(c.off, window.OnResize(c.layout), window.OnShow(c.refresh), window.OnHide(c.refresh), window.OnMinimize(c.refresh), window.OnRestore(c.refresh), window.OnClosed(c.Close))
	})
	if c == nil && err == nil {
		err = errors.New("playback requires a running MyGo event loop")
	}
	return c, err
}

// SetSource selects a validated local source, preserving the window observers.
// A player is created only when the source can play. Empty source unloads video.
func (c *Controller) SetSource(path string) error {
	if path != "" {
		if !filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
			return errors.New("video source must be an absolute local path")
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("video source: %w", err)
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return errors.New("video source must be a nonempty regular file")
		}
	}
	var err error
	mygo.RunOnMain(func() {
		if c.closed {
			err = errors.New("video controller is closed")
			return
		}
		if path == c.source {
			return
		}
		c.unload()
		c.source, c.failure = path, nil
		c.refresh()
		err = c.failure
	})
	return err
}

// load creates the decoder only when refresh permits playback. Main thread only.
func (c *Controller) load() error {
	url := send(class("NSURL"), "fileURLWithPath:", nsString(c.source))
	item := send(class("AVPlayerItem"), "playerItemWithURL:", url)
	c.player = send(send(class("AVQueuePlayer"), "alloc"), "init")
	if item == 0 || c.player == 0 {
		c.unload()
		return errors.New("create AVFoundation movie player")
	}
	send(c.player, "setMuted:", true)
	send(c.player, "setPreventsDisplaySleepDuringVideoPlayback:", false)
	c.looper = send(class("AVPlayerLooper"), "playerLooperWithPlayer:templateItem:", c.player, item)
	if c.looper == 0 {
		c.unload()
		return errors.New("create AVPlayerLooper")
	}
	send(c.looper, "retain")
	send(c.layer, "setPlayer:", c.player)
	return nil
}

func (c *Controller) SetPageVisible(visible bool) {
	mygo.RunOnMain(func() {
		if !c.closed {
			c.page = visible
			c.refresh()
		}
	})
}
func (c *Controller) SetGameRunning(running bool) {
	mygo.RunOnMain(func() {
		if !c.closed {
			c.game = running
			c.refresh()
		}
	})
}
func (c *Controller) SetPaused(paused bool) {
	mygo.RunOnMain(func() {
		if !c.closed {
			c.paused = paused
			c.refresh()
		}
	})
}

// SetSidebarWidth remains source-compatible. Backgrounds now fill the entire
// window; sidebar geometry belongs to SetMaterials rather than video cropping.
func (c *Controller) SetSidebarWidth(float64) {}

func (c *Controller) layout() {
	if c.closed {
		return
	}
	bounds := value[rectangle](c.content, "bounds")
	send(class("CATransaction"), "begin")
	send(class("CATransaction"), "setDisableActions:", true)
	if bounds != c.backgroundBounds {
		nativeAPI.setRect(c.host, objc.RegisterName("setFrame:"), rectangle{Width: bounds.Width, Height: bounds.Height})
		nativeAPI.setRect(c.posterHost, objc.RegisterName("setFrame:"), rectangle{Width: bounds.Width, Height: bounds.Height})
		c.backgroundBounds = bounds
	}
	c.layoutMaterials(bounds.Height)
	send(class("CATransaction"), "commit")
}

func (c *Controller) visible() bool {
	return value[bool](c.native, "isVisible") && !value[bool](c.native, "isMiniaturized") &&
		value[uint64](c.native, "occlusionState")&2 != 0 && !value[bool](send(class("NSApplication"), "sharedApplication"), "isHidden") &&
		!c.locked && !c.sleeping && !c.screensSleeping && !c.sessionInactive
}

func (c *Controller) reason() string {
	switch {
	case c.closed:
		return "closed"
	case c.failure != nil:
		return "error"
	case c.source == "":
		return "no-source"
	case !c.page || !c.backgroundVisible:
		return "other-page"
	case c.paused:
		return "user-paused"
	case c.game:
		return "game-running"
	case !c.visible():
		return "window-not-visible"
	default:
		return "playing"
	}
}

func (c *Controller) refresh() {
	if c.closed {
		return
	}
	c.checkError()
	// A different page never displays the movie. Hidden windows and games get
	// a short grace period, so a brief interruption can reuse the last frame.
	// Status's existing low-frequency polling expires this grace period.
	switch {
	case !c.page || !c.backgroundVisible:
		if c.player != 0 {
			c.unload()
		}
	case c.game || !c.visible():
		if c.player != 0 {
			if c.inactiveSince.IsZero() {
				c.inactiveSince = time.Now()
			} else if time.Since(c.inactiveSince) >= 15*time.Second {
				c.unload()
			}
		}
	default:
		c.inactiveSince = time.Time{}
	}
	playing := c.reason() == "playing"
	if playing && c.player == 0 {
		if err := c.load(); err != nil {
			c.fail(err)
			playing = false
		}
	}
	if playing != c.playing {
		if playing {
			send(c.player, "play")
		} else {
			send(c.player, "pause")
		}
		c.playing = playing
	}
	// Keep the last native frame displayed while paused. Other pages hide it.
	showVideo := c.backgroundVisible && c.page && c.player != 0 && c.failure == nil && value[bool](c.layer, "isReadyForDisplay")
	send(c.host, "setHidden:", !showVideo)
	send(c.posterHost, "setHidden:", !c.backgroundVisible || c.posterImage == 0)
	c.refreshMaterials()
}

func (c *Controller) checkError() {
	if c.player == 0 || c.failure != nil {
		return
	}
	item := send(c.player, "currentItem")
	for _, object := range []objc.ID{c.player, item, c.looper} {
		if object == 0 {
			continue
		}
		if value[int64](object, "status") != 2 {
			continue
		}
		errorObject := send(object, "error")
		message := "AVFoundation playback failed"
		if errorObject != 0 {
			message = value[string](send(errorObject, "localizedDescription"), "UTF8String")
		}
		c.fail(errors.New(message))
		return
	}
}

func (c *Controller) fail(err error) {
	c.failure = err
	// A notification may coalesce while a consumer is busy. Status.Error
	// always retains the current failure, without blocking the UI thread.
	select {
	case c.errors <- err:
	default:
	}
}

// Errors delivers asynchronous AVFoundation failures. Read it until Close.
func (c *Controller) Errors() <-chan error { return c.errors }

// Status also checks asynchronous AVFoundation failures. Polling this once per
// second is sufficient for UI state; native visibility changes are event driven.
func (c *Controller) Status() Status {
	var status Status
	mygo.RunOnMain(func() {
		if c.closed {
			status.Reason = "closed"
			return
		}
		c.refresh()
		status = Status{Source: c.source, Loaded: c.player != 0, Playing: c.playing, Visible: c.visible(), PageVisible: c.page, Paused: c.paused, GameRunning: c.game, Error: c.failure, Reason: c.reason(), BackdropReady: c.posterImage != 0, BackdropKey: c.backdropKey}
		if c.player != 0 {
			status.Rate = value[float32](c.player, "rate")
			status.ItemStatus = int(value[int64](send(c.player, "currentItem"), "status"))
			status.ReadyForDisplay = value[bool](c.layer, "isReadyForDisplay")
		}
	})
	return status
}

func (c *Controller) unload() {
	if c.player != 0 {
		send(c.player, "pause")
	}
	send(c.layer, "setPlayer:", objc.ID(0))
	if c.looper != 0 {
		send(c.looper, "disableLooping")
	}
	if c.player != 0 {
		send(c.player, "removeAllItems")
	}
	release(c.looper)
	release(c.player)
	c.looper, c.player, c.playing = 0, 0, false
	c.inactiveSince = time.Time{}
}

func (c *Controller) Close() { mygo.RunOnMain(c.close) }
func (c *Controller) close() {
	if c.closed {
		return
	}
	c.closed = true
	for _, off := range c.off {
		off()
	}
	c.off = nil
	for _, observer := range c.observers {
		send(observer.center, "removeObserver:", observer.token)
		release(observer.token)
	}
	c.observers = nil
	c.unload()
	c.closeBackdrop()
	send(c.host, "removeFromSuperview")
	release(c.host)
	c.host = 0
	release(c.layer)
	c.layer = 0
	close(c.errors)
}

func sessionLocked() bool {
	dictionary := nativeAPI.session()
	if dictionary == 0 {
		return true
	} // A disconnected login session cannot display video.
	defer nativeAPI.cfRelease(dictionary)
	key := nativeAPI.cfString(0, "CGSSessionScreenIsLocked", 0x08000100)
	defer nativeAPI.cfRelease(key)
	value := nativeAPI.dictGet(dictionary, key)
	return value != 0 && nativeAPI.boolean(value)
}

func (c *Controller) observe() error {
	standard := send(class("NSNotificationCenter"), "defaultCenter")
	workspace := send(send(class("NSWorkspace"), "sharedWorkspace"), "notificationCenter")
	distributed := send(class("NSDistributedNotificationCenter"), "defaultCenter")
	type event struct {
		center objc.ID
		name   string
		action func()
	}
	events := []event{
		{standard, "NSWindowDidChangeOcclusionStateNotification", func() {}},
		{standard, "NSWindowDidMiniaturizeNotification", func() {}},
		{standard, "NSWindowDidDeminiaturizeNotification", func() {}},
		{standard, "NSApplicationDidHideNotification", func() {}},
		{standard, "NSApplicationDidUnhideNotification", func() {}},
		{workspace, "NSWorkspaceWillSleepNotification", func() { c.sleeping = true }},
		{workspace, "NSWorkspaceDidWakeNotification", func() { c.sleeping = false; c.locked = sessionLocked() }},
		{workspace, "NSWorkspaceScreensDidSleepNotification", func() { c.screensSleeping = true }},
		{workspace, "NSWorkspaceScreensDidWakeNotification", func() { c.screensSleeping = false; c.locked = sessionLocked() }},
		{workspace, "NSWorkspaceSessionDidResignActiveNotification", func() { c.sessionInactive = true }},
		{workspace, "NSWorkspaceSessionDidBecomeActiveNotification", func() { c.sessionInactive = false; c.locked = sessionLocked() }},
		{distributed, "com.apple.screenIsLocked", func() { c.locked = true }},
		{distributed, "com.apple.screenIsUnlocked", func() { c.locked = sessionLocked() }},
	}
	for _, event := range events {
		if event.center == 0 {
			return fmt.Errorf("notification center unavailable for %s", event.name)
		}
		block := objc.NewBlock(func(_ objc.Block, _ objc.ID) {
			mygo.RunOnMain(func() {
				if !c.closed {
					event.action()
					c.refresh()
				}
			})
		})
		queue := send(class("NSOperationQueue"), "mainQueue")
		token := send(event.center, "addObserverForName:object:queue:usingBlock:", nsString(event.name), objc.ID(0), queue, block)
		block.Release()
		if token == 0 {
			return fmt.Errorf("observe %s", event.name)
		}
		send(token, "retain")
		c.observers = append(c.observers, observation{event.center, token})
	}
	return nil
}
