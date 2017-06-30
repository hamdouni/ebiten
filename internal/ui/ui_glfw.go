// Copyright 2015 Hajime Hoshi
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// +build darwin freebsd linux windows
// +build !js
// +build !android
// +build !ios

package ui

import (
	"errors"
	"runtime"
	"sync"
	"time"

	"github.com/go-gl/glfw/v3.2/glfw"
	"github.com/hajimehoshi/ebiten/internal/opengl"
)

type userInterface struct {
	title           string
	window          *glfw.Window
	width           int
	height          int
	scale           float64
	deviceScale     float64
	glfwScale       float64
	fullscreen      bool
	fullscreenScale float64
	funcs           chan func()
	running         bool
	sizeChanged     bool
	origPosX        int
	origPosY        int
	m               sync.Mutex
}

var currentUI *userInterface

func init() {
	if err := initialize(); err != nil {
		panic(err)
	}
}

func initialize() error {
	runtime.LockOSThread()

	if err := glfw.Init(); err != nil {
		return err
	}
	glfw.WindowHint(glfw.Visible, glfw.False)
	glfw.WindowHint(glfw.Resizable, glfw.False)
	glfw.WindowHint(glfw.ContextVersionMajor, 2)
	glfw.WindowHint(glfw.ContextVersionMinor, 1)

	// As start, create an window with temporary size to create OpenGL context thread.
	window, err := glfw.CreateWindow(16, 16, "", nil, nil)
	if err != nil {
		return err
	}
	hideConsoleWindowOnWindows()
	u := &userInterface{
		window:      window,
		funcs:       make(chan func()),
		sizeChanged: true,
		origPosX:    -1,
		origPosY:    -1,
	}
	u.window.MakeContextCurrent()
	glfw.SwapInterval(1)
	currentUI = u
	return nil
}

func RunMainThreadLoop(ch <-chan error) error {
	// TODO: Check this is done on the main thread.
	currentUI.setRunning(true)
	defer func() {
		currentUI.setRunning(false)
	}()
	for {
		select {
		case f := <-currentUI.funcs:
			f()
		case err := <-ch:
			// ch returns a value not only when an error occur but also it is closed.
			return err
		}
	}
}

func (u *userInterface) isRunning() bool {
	u.m.Lock()
	defer u.m.Unlock()
	return u.running
}

func (u *userInterface) setRunning(running bool) {
	u.m.Lock()
	defer u.m.Unlock()
	u.running = running
}

func (u *userInterface) runOnMainThread(f func() error) error {
	if u.funcs == nil {
		// already closed
		return nil
	}
	ch := make(chan struct{})
	var err error
	u.funcs <- func() {
		err = f()
		close(ch)
	}
	<-ch
	return err
}

func SetScreenSize(width, height int) bool {
	u := currentUI
	if !u.isRunning() {
		panic("ui: Run is not called yet")
	}
	r := false
	_ = u.runOnMainThread(func() error {
		r = u.setScreenSize(width, height, u.scale, u.fullscreen)
		return nil
	})
	return r
}

func SetScreenScale(scale float64) bool {
	u := currentUI
	if !u.isRunning() {
		panic("ui: Run is not called yet")
	}
	r := false
	_ = u.runOnMainThread(func() error {
		r = u.setScreenSize(u.width, u.height, scale, u.fullscreen)
		return nil
	})
	return r
}

func SetFullscreen(fullscreen bool) bool {
	u := currentUI
	if !u.isRunning() {
		panic("ui: Run is not called yet")
	}
	r := false
	_ = u.runOnMainThread(func() error {
		r = u.setScreenSize(u.width, u.height, u.scale, fullscreen)
		return nil
	})
	return r
}

func ScreenScale() float64 {
	u := currentUI
	if !u.isRunning() {
		return 0
	}
	s := 0.0
	_ = u.runOnMainThread(func() error {
		s = u.scale
		return nil
	})
	return s
}

func IsFullscreen() bool {
	u := currentUI
	if !u.isRunning() {
		return false
	}
	f := false
	_ = u.runOnMainThread(func() error {
		f = u.fullscreen
		return nil
	})
	return f
}

func SetCursorVisibility(visible bool) {
	// This can be called before Run: change the state asyncly.
	go func() {
		_ = currentUI.runOnMainThread(func() error {
			c := glfw.CursorNormal
			if !visible {
				c = glfw.CursorHidden
			}
			currentUI.window.SetInputMode(glfw.CursorMode, c)
			return nil
		})
	}()
}

func Run(width, height int, scale float64, title string, g GraphicsContext) error {
	u := currentUI
	// GLContext must be created before setting the screen size, which requires
	// swapping buffers.
	opengl.Init(currentUI.runOnMainThread)
	if err := u.runOnMainThread(func() error {
		m := glfw.GetPrimaryMonitor()
		v := m.GetVideoMode()
		if !u.setScreenSize(width, height, scale, false) {
			return errors.New("ui: Fail to set the screen size")
		}
		u.title = title
		u.window.SetTitle(title)
		u.window.Show()

		w, h := u.glfwSize()
		x := (v.Width - w) / 2
		y := (v.Height - h) / 3
		x, y = adjustWindowPosition(x, y)
		u.window.SetPos(x, y)
		return nil
	}); err != nil {
		return err
	}
	return u.loop(g)
}

func (u *userInterface) glfwSize() (int, int) {
	if u.glfwScale == 0 {
		u.glfwScale = glfwScale()
	}
	return int(float64(u.width) * u.scale * u.glfwScale), int(float64(u.height) * u.scale * u.glfwScale)
}

func (u *userInterface) getScale() float64 {
	if !u.fullscreen {
		return u.scale
	}
	if u.fullscreenScale == 0 {
		if u.glfwScale == 0 {
			u.glfwScale = glfwScale()
		}
		m := glfw.GetPrimaryMonitor()
		v := m.GetVideoMode()
		sw := float64(v.Width) / u.glfwScale / float64(u.width)
		sh := float64(v.Height) / u.glfwScale / float64(u.height)
		s := sw
		if s > sh {
			s = sh
		}
		u.fullscreenScale = s
	}
	return u.fullscreenScale
}

func (u *userInterface) actualScreenScale() float64 {
	if u.deviceScale == 0 {
		u.deviceScale = deviceScale()
	}
	return u.getScale() * u.deviceScale
}

func (u *userInterface) pollEvents() {
	glfw.PollEvents()
	if u.glfwScale == 0 {
		u.glfwScale = glfwScale()
	}
	currentInput.update(u.window, u.getScale()*u.glfwScale)
}

func (u *userInterface) update(g GraphicsContext) error {
	shouldClose := false
	_ = u.runOnMainThread(func() error {
		shouldClose = u.window.ShouldClose()
		return nil
	})
	if shouldClose {
		return &RegularTermination{}
	}

	actualScale := 0.0
	sizeChanged := false
	_ = u.runOnMainThread(func() error {
		if !u.sizeChanged {
			return nil
		}
		u.sizeChanged = false
		actualScale = u.actualScreenScale()
		sizeChanged = true
		return nil
	})
	if sizeChanged {
		g.SetSize(u.width, u.height, actualScale)
	}

	_ = u.runOnMainThread(func() error {
		u.pollEvents()
		for u.window.GetAttrib(glfw.Focused) == 0 {
			// Wait for an arbitrary period to avoid busy loop.
			time.Sleep(time.Second / 60)
			u.pollEvents()
			if u.window.ShouldClose() {
				return nil
			}
		}
		return nil
	})
	if err := g.Update(); err != nil {
		return err
	}
	return nil
}

func (u *userInterface) loop(g GraphicsContext) error {
	defer func() {
		_ = u.runOnMainThread(func() error {
			glfw.Terminate()
			return nil
		})
	}()
	for {
		if err := u.update(g); err != nil {
			return err
		}
		// The bound framebuffer must be the default one (0) before swapping buffers.
		if err := opengl.GetContext().BindScreenFramebuffer(); err != nil {
			return err
		}
		_ = u.runOnMainThread(func() error {
			u.swapBuffers()
			return nil
		})
	}
}

func (u *userInterface) swapBuffers() {
	u.window.SwapBuffers()
}

func (u *userInterface) setScreenSize(width, height int, scale float64, fullscreen bool) bool {
	if u.width == width && u.height == height && u.scale == scale && u.fullscreen == fullscreen {
		return false
	}

	origScale := u.scale
	u.scale = scale

	// On Windows, giving a too small width doesn't call a callback (#165).
	// To prevent hanging up, return asap if the width is too small.
	// 252 is an arbitrary number and I guess this is small enough.
	// TODO: The same check should be in ui_js.go
	const minWindowWidth = 252
	if int(float64(width)*u.actualScreenScale()) < minWindowWidth {
		u.scale = origScale
		return false
	}
	if u.width != width || u.height != height {
		u.width = width
		u.height = height
		u.fullscreenScale = 0
	}

	// To make sure the current existing framebuffers are rendered,
	// swap buffers here before SetSize is called.
	u.swapBuffers()

	u.fullscreen = fullscreen

	window := u.window
	m := glfw.GetPrimaryMonitor()
	v := m.GetVideoMode()
	if u.fullscreen {
		if u.origPosX < 0 && u.origPosY < 0 {
			u.origPosX, u.origPosY = window.GetPos()
		}
		window.SetMonitor(m, 0, 0, v.Width, v.Height, v.RefreshRate)
	} else {
		if u.origPosX >= 0 && u.origPosY >= 0 {
			x := u.origPosX
			y := u.origPosY
			window.SetMonitor(nil, x, y, 16, 16, v.RefreshRate)
			u.origPosX = -1
			u.origPosY = -1
		}
		ch := make(chan struct{})
		window.SetFramebufferSizeCallback(func(_ *glfw.Window, width, height int) {
			window.SetFramebufferSizeCallback(nil)
			close(ch)
		})
		w, h := u.glfwSize()
		window.SetSize(w, h)
	event:
		for {
			glfw.PollEvents()
			select {
			case <-ch:
				break event
			default:
			}
		}
		// Window title might lost on macOS after coming back from fullscreen.
		u.window.SetTitle(u.title)
	}
	// TODO: Rename this variable?
	u.sizeChanged = true
	return true
}
