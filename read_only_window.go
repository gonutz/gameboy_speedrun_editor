package main

import "github.com/gonutz/prototype/draw"

func newReadOnlyWindow(window draw.Window) draw.Window {
	return readOnlyWindow{Window: window}
}

type readOnlyWindow struct {
	draw.Window
}

func (w readOnlyWindow) WasKeyPressed(key draw.Key) bool {
	return false
}

func (w readOnlyWindow) IsKeyDown(key draw.Key) bool {
	return false
}

func (w readOnlyWindow) Characters() string {
	return ""
}

func (w readOnlyWindow) IsMouseDown(button draw.MouseButton) bool {
	return false
}

func (w readOnlyWindow) Clicks() []draw.MouseClick {
	return nil
}

func (w readOnlyWindow) MousePosition() (x, y int) {
	return -1, -1
}

func (w readOnlyWindow) MouseWheelX() float64 {
	return 0
}

func (w readOnlyWindow) MouseWheelY() float64 {
	return 0
}

func (w readOnlyWindow) PlaySoundFile(path string) error {
	return nil
}
