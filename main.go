package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gonutz/prototype/draw"
	"github.com/sqweek/dialog"
)

var (
	mute       = flag.Bool("mute", false, "mute sound output")
	cpuprofile = flag.Bool("cpuprofile", false, "write cpu profile to file (debugging)")
)

var keyMap = map[draw.Key]Button{
	draw.KeyL: ButtonLeft,
	draw.KeyU: ButtonUp,
	draw.KeyR: ButtonRight,
	draw.KeyD: ButtonDown,
	draw.KeyA: ButtonA,
	draw.KeyB: ButtonB,
	draw.KeyS: ButtonStart,
	draw.KeyE: ButtonSelect,
}

const (
	keyFrameInterval   = 100
	sessionFileVersion = 1

	textScale   = 0.8
	fontHeight  = 13
	frameWidth  = 1 + ScreenWidth + 1
	frameHeight = fontHeight + ScreenHeight + 1
)

func main() {
	flag.Parse()

	if *cpuprofile {
		startProfiling()
		defer stopProfiling()
	}

	var err error
	globalROM, err = getRom()
	check(err)

	state := newEditorState()
	state.loadLastSpeedrun()
	defer state.saveCurrentSpeedrun()

	check(draw.RunWindow("Gameboy Speedrun Editor", 1500, 800, func(window draw.Window) {
		windowW, windowH := window.Size()
		defer func() {
			state.lastWindowW, state.lastWindowH = windowW, windowH
		}()

		if window.WasKeyPressed(draw.KeyF11) || window.WasKeyPressed(draw.KeyF) {
			state.fullscreen = !state.fullscreen
			window.SetFullscreen(state.fullscreen)
		}

		goToEditor := state.replayingGame && window.WasKeyPressed(draw.KeyEscape)
		if goToEditor {
			state.replayingGame = false
			state.lastReplayPaused = state.replayPaused
			state.resetInfoText()
			muteSound()
			state.render()
		}

		goToGameReplay := !state.replayingGame && window.WasKeyPressed(draw.KeySpace)
		if goToGameReplay {
			state.replayingGame = true

			// NOTE We set the pause state to the opposite of what we want
			// it to be because the same key (SPACE) is used to toggle both
			// the replay state and the pause state. That means when we hit
			// that key, we go to the editor and it will immediately toggle
			// the pause state because that button is still down.
			state.replayPaused = !state.lastReplayPaused

			// TODO Do not clear the cache outside of setDirtyFrame any longer.
			// Once we go through generateFrame and setDirtyFrame for the editor
			// as well, we can remove this line.
			state.frameCache.clear()

			state.lastReplayedFrame = state.leftMostFrame
			state.render()
		}

		if state.replayingGame {
			state.executeReplayFrame(window)
		} else {
			state.executeEditorFrame(window)
		}
	}))
}

func newEditorState() *editorState {
	return &editorState{
		dragStartFrame:          -1,
		frameCache:              newFrameCache(),
		pendingDoubleClickFrame: -1,
		draggingFrameIndex:      -1,
		infoTextColor:           draw.White,
		screenDirty:             true,
	}
}

type editorState struct {
	leftMostFrame   int
	activeSelection frameSelection
	frameInputs     []inputState // Holds the state of all the Gameboy buttons for each frame.
	defaultInputs   inputState   // Button states for future frames that are not yet generated.
	// keyFrameStates are the states at every keyFrameInterval-th frame. The
	// very first item in keyFrameStates is for frame 0.
	keyFrameStates []Gameboy

	frameCache          *frameCache
	singleScreenBuffer  [4 * ScreenWidth * ScreenHeight]byte
	gameboyScreenBuffer []byte
	// We generate Gameboy screens to be display in our editor.
	// screenBuffer is a temporary buffer that we reuse in every frame.
	screenBuffer []gameboyScreen
	screenDirty  bool
	lastWindowW  int
	lastWindowH  int
	fullscreen   bool

	// dragStart... are for dragging frame inputs.
	dragStartFrame     int
	dragStartSelection frameSelection
	dragStartInputs    []inputState

	doubleClickPending      bool
	pendingDoubleClickFrame int
	controlWasDown          bool
	keyRepeatCountdown      int
	// draggingFrameIndex is for moving the current position in time (the
	// left-most visible frame). It is NOT for dragging inputs.
	draggingFrameIndex int
	lastLeftClick      mouseClick
	lastAction         inputAction

	// We can toggle between the editor which freezes time and shows multiple
	// frames at once and running the emulator which replays the game in
	// real-time using our edited inputs.
	replayingGame     bool
	replayPaused      bool
	lastReplayPaused  bool
	lastReplayedFrame int

	infoText      string
	infoTextColor draw.Color
}

func (s *editorState) setInfo(msg string) {
	s.infoText = msg
	s.infoTextColor = draw.RGBA(1, 1, 1, 1)
}

func (s *editorState) setWarning(msg string) {
	s.infoText = msg
	s.infoTextColor = draw.RGBA(1, 92/255.0, 92/255.0, 1)
}

func (s *editorState) resetInfoText() {
	s.infoText = ""
}

func (s *editorState) render() {
	s.screenDirty = true
}

func (s *editorState) updateGameboy(gameboy *Gameboy, frameIndex int) {
	var inputs inputState

	if s.replayingGame {
		// While in game if we have used up the last inputs, we forever
		// press the default inputs.
		if frameIndex < len(s.frameInputs) {
			inputs = s.frameInputs[frameIndex]
		} else {
			inputs = s.defaultInputs
		}
	} else {
		// In the editor, we extend the frame inputs of we reach their end.
		for frameIndex >= len(s.frameInputs) {
			s.frameInputs = append(s.frameInputs, s.defaultInputs)
		}

		inputs = s.frameInputs[frameIndex]
	}

	for b := range buttonCount {
		if isButtonDown(inputs, b) {
			gameboy.PressButton(b)
		} else {
			gameboy.ReleaseButton(b)
		}
	}

	gameboy.Update()
}

func (s *editorState) generateFrame(frameIndex int) Gameboy {
	if s.frameCache.contains(frameIndex) {
		return s.frameCache.at(frameIndex)
	}

	if s.frameCache.contains(frameIndex - 1) {
		gb := s.frameCache.at(frameIndex - 1)
		s.updateGameboy(&gb, frameIndex)

		s.frameCache.set(frameIndex, gb)

		if frameIndex%keyFrameInterval == 0 &&
			(frameIndex/keyFrameInterval) == len(s.keyFrameStates) {
			s.keyFrameStates = append(s.keyFrameStates, gb)
		}

		return gb
	}

	// Go from the latest key frame before this frame.
	keyFrameIndex := frameIndex / keyFrameInterval

	// Create as many key frames as we need.
	for keyFrameIndex >= len(s.keyFrameStates) {
		last := len(s.keyFrameStates) - 1

		if last == -1 {
			gb := NewGameboy(globalROM, GameboyOptions{})
			s.updateGameboy(&gb, 0)
			s.keyFrameStates = append(s.keyFrameStates, gb)
		} else {
			gb := s.keyFrameStates[last]
			for i := range keyFrameInterval {
				s.updateGameboy(&gb, last*keyFrameInterval+i+1)
			}
			s.keyFrameStates = append(s.keyFrameStates, gb)
		}
	}

	// Now the key frame we need exists. We start from there, create frames up
	// to where we want to go, while putting those frames in the cache as well.
	gb := s.keyFrameStates[keyFrameIndex]

	// Emulate frames until we reach our destination.
	currentIndex := keyFrameIndex * keyFrameInterval
	for currentIndex < frameIndex {
		s.updateGameboy(&gb, currentIndex+1)
		currentIndex++
		s.frameCache.set(currentIndex, gb)
	}

	return gb
}

func (s *editorState) setDirtyFrame(frameIndex int) {
	// We can only keep past key frames that are not dirty:
	//
	// frame index | number of key frames to keep
	// ------------+-----------------------------
	//           0 | 0 (key frame 0 is the first ever frame, so if it changes, delete all key frames)
	//           1 | 1 (changing frame 1 leaves frame 0 (the very first key frame) intact)
	//         ... | 1
	//         100 | 1 (changing frame 100 leaves frames 0..99 intact - keep key frame 0)
	//         101 | 2 (changing frame 101 leaves frames 0..100 inteact - keep key frames 0 and 1)
	//         ... | 2
	//         200 | 2
	//         201 | 3
	//
	keep := (frameIndex + keyFrameInterval - 1) / keyFrameInterval
	if keep < len(s.keyFrameStates) {
		s.keyFrameStates = s.keyFrameStates[:keep]
	}

	s.frameCache.removeFramesStartingAt(frameIndex)
}

// TODO Possible optimization: give toggleButton a range instead of a single
// frame to not have to call setDirtyFrame all the time. This will be useful for
// the editor, where we change buttons on multiple frames at once.
func (state *editorState) toggleButton(frameIndex int, button Button) {
	for frameIndex >= len(state.frameInputs) {
		state.frameInputs = append(state.frameInputs, state.defaultInputs)
	}

	toggleButton(&state.frameInputs[frameIndex], button)
	state.setDirtyFrame(frameIndex)
}

func (s *editorState) isButtonDown(frameIndex int, button Button) bool {
	if frameIndex < len(s.frameInputs) {
		return isButtonDown(s.frameInputs[frameIndex], button)
	}
	return isButtonDown(s.defaultInputs, button)
}

func (state *editorState) setButtonDown(frameIndex, count int, button Button, down bool) {
	end := frameIndex + count - 1
	for end >= len(state.frameInputs) {
		state.frameInputs = append(state.frameInputs, state.defaultInputs)
	}

	for i := range count {
		setButtonDown(&state.frameInputs[frameIndex+i], button, down)
	}

	state.setDirtyFrame(frameIndex)
}

func (state *editorState) executeReplayFrame(window draw.Window) {
	windowW, windowH := window.Size()
	mouseX, mouseY := window.MousePosition()
	leftClick := wasLeftClicked(window)

	if window.WasKeyPressed(draw.KeySpace) {
		state.replayPaused = !state.replayPaused
		if state.replayPaused {
			muteSound()
		} else {
			unmuteSound()
		}
	}

	// Let the user toggle buttons for the current frame.
	for key, b := range keyMap {
		if window.WasKeyPressed(key) {
			state.toggleButton(state.lastReplayedFrame, b)
		}
	}

	// When replay is paused, we use a key repeat counter to skip through single
	// frames in stop-motion.
	// While replaying (non-paused) simply holding down a key will change the
	// speed.
	state.keyRepeatCountdown--
	keyTriggered := func(key draw.Key) bool {
		if state.replayPaused {
			if window.WasKeyPressed(key) ||
				window.IsKeyDown(key) && state.keyRepeatCountdown <= 0 {
				state.keyRepeatCountdown = 10
				return true
			}
			return false
		} else {
			return window.IsKeyDown(key)
		}
	}

	// Handle keys to accelerate/decelerate the playback.
	nextFrameIndex := state.lastReplayedFrame + 1

	if state.replayPaused {
		nextFrameIndex = state.lastReplayedFrame
	}

	if window.WasKeyPressed(draw.KeyHome) {
		nextFrameIndex = 0
	} else if keyTriggered(draw.KeyLeft) {
		nextFrameIndex = max(0, state.lastReplayedFrame-1)
	} else if keyTriggered(draw.KeyUp) {
		nextFrameIndex = max(0, state.lastReplayedFrame-5)
	} else if keyTriggered(draw.KeyPageUp) {
		nextFrameIndex = max(0, state.lastReplayedFrame-20)
	} else if keyTriggered(draw.KeyRight) {
		if state.replayPaused {
			nextFrameIndex = state.lastReplayedFrame + 1
		} else {
			nextFrameIndex = state.lastReplayedFrame + 2
		}
	} else if keyTriggered(draw.KeyDown) {
		nextFrameIndex = state.lastReplayedFrame + 5
	} else if keyTriggered(draw.KeyPageDown) {
		nextFrameIndex = state.lastReplayedFrame + 20
	}

	gb := state.generateFrame(nextFrameIndex)
	state.lastReplayedFrame = nextFrameIndex

	// Render the current screen.
	window.CreateImage("gameboyScreen", ScreenWidth, ScreenHeight)
	i := 0
	for y := range ScreenHeight {
		for x := range ScreenWidth {
			color := gb.PreparedData[x][y]
			state.singleScreenBuffer[i+0] = color[0]
			state.singleScreenBuffer[i+1] = color[1]
			state.singleScreenBuffer[i+2] = color[2]
			state.singleScreenBuffer[i+3] = 255
			i += 4
		}
	}
	window.SetImagePixels("gameboyScreen", state.singleScreenBuffer[:])

	window.FillRect(0, 0, windowW, windowH, toColor(ColorPalette[3]))

	const (
		inputMenuW      = 200
		inputMenuMargin = 20
	)

	// Letterbox the Gameboy screen into our window.
	xScale := float64(windowW-inputMenuW-inputMenuMargin) / ScreenWidth
	yScale := float64(windowH) / ScreenHeight
	scale := math.Min(yScale, xScale)
	screenW := round(scale * ScreenWidth)
	screenH := round(scale * ScreenHeight)
	screenX := (windowW - inputMenuW - inputMenuMargin - screenW) / 2
	screenY := (windowH - screenH) / 2
	window.DrawImageFileTo("gameboyScreen", screenX, screenY, screenW, screenH, 0)

	// Draw the inputs as a menu.
	inputs := state.defaultInputs
	if state.lastReplayedFrame < len(state.frameInputs) {
		inputs = state.frameInputs[state.lastReplayedFrame]
	}

	const (
		abButtonSize   = 75
		abButtonSpaceX = abButtonSize / 8
		hoverMargin    = 10
	)

	_, baseFontHeight := window.GetTextSize("|")
	hoverColor := draw.RGBA(0, 0.5, 0, 0.3)

	// Clear the menu background.
	inputMenuX := screenX + screenW + inputMenuMargin
	window.FillRect(inputMenuX, 0, inputMenuW, windowH, rgb(224, 248, 208))

	drawAB := func(r rectangle, text string, button Button) {
		textColor := draw.Gray
		backColor := draw.DarkRed
		if isButtonDown(inputs, button) {
			textColor = draw.White
			backColor = draw.Red
		}

		textScale := abButtonSize * 0.9 / float32(baseFontHeight)
		textW, textH := window.GetScaledTextSize(text, textScale)

		r.fillEllipse(window, backColor)
		window.DrawScaledText(
			text,
			r.x+(r.w-textW)/2,
			r.y+(r.h-textH)/2,
			textScale,
			textColor,
		)
		centerX := r.x + abButtonSize/2
		centerY := r.y + abButtonSize/2
		radius := abButtonSize / 2
		hovering := square(mouseX-centerX)+square(mouseY-centerY) <= square(radius)
		if hovering {
			window.FillEllipse(
				r.x-hoverMargin,
				r.y-hoverMargin,
				r.w+2*hoverMargin,
				r.h+2*hoverMargin,
				hoverColor,
			)
			if leftClick {
				state.toggleButton(state.lastReplayedFrame, button)
			}
		}
	}

	bButtonX := inputMenuX + (inputMenuW-(abButtonSize+abButtonSpaceX+abButtonSize))/2
	aButtonX := bButtonX + abButtonSize + abButtonSpaceX
	aButtonY := abButtonSize / 2
	bButtonY := aButtonY + abButtonSize/2

	drawAB(rect(aButtonX, aButtonY, abButtonSize, abButtonSize), "A", ButtonA)
	drawAB(rect(bButtonX, bButtonY, abButtonSize, abButtonSize), "B", ButtonB)

	// Draw the D-Pad.
	const dpadButtonSize = abButtonSize * 7 / 10
	dpadX := inputMenuX + (inputMenuW-3*dpadButtonSize)/2
	dpadY := bButtonY + abButtonSize + dpadButtonSize
	window.FillRect(
		dpadX+dpadButtonSize,
		dpadY,
		dpadButtonSize,
		3*dpadButtonSize,
		draw.Black,
	)
	window.FillRect(
		dpadX,
		dpadY+dpadButtonSize,
		3*dpadButtonSize,
		dpadButtonSize,
		draw.Black,
	)
	drawPressedDPad := func(button Button, x, y int, text string) {
		r := rect(x, y, dpadButtonSize, dpadButtonSize)
		innerR := r.expand(-5)
		outerR := r.expand(hoverMargin)

		textColor := draw.White
		if isButtonDown(inputs, button) {
			innerR.fill(window, draw.LightGray)
			textColor = draw.Black
		}

		textScale := 0.8 * dpadButtonSize / float32(baseFontHeight)
		textW, textH := window.GetScaledTextSize(text, textScale)
		textX := x + (dpadButtonSize-textW)/2
		textY := y + (dpadButtonSize-textH)/2
		window.DrawScaledText(text, textX, textY, textScale, textColor)

		hoveringOverButton := rect(x, y, dpadButtonSize, dpadButtonSize).contains(mouseX, mouseY)
		if hoveringOverButton {
			outerR.fill(window, hoverColor)
			if leftClick {
				state.toggleButton(state.lastReplayedFrame, button)
			}
		}
	}
	drawPressedDPad(ButtonLeft, dpadX, dpadY+dpadButtonSize, "L")
	drawPressedDPad(ButtonUp, dpadX+dpadButtonSize, dpadY, "U")
	drawPressedDPad(ButtonRight, dpadX+2*dpadButtonSize, dpadY+dpadButtonSize, "R")
	drawPressedDPad(ButtonDown, dpadX+dpadButtonSize, dpadY+2*dpadButtonSize, "D")

	// Draw Start and Select buttons.
	drawStartSelect := func(r rectangle, text string, button Button) {
		backColor := draw.Gray
		textColor := draw.LightGray
		if isButtonDown(inputs, button) {
			backColor = draw.LightGray
			textColor = draw.Black
		}

		window.FillRect(r.x+r.h/2, r.y, r.w-r.h, r.h, backColor)
		window.FillEllipse(r.x, r.y, r.h, r.h, backColor)
		window.FillEllipse(r.x+r.w-r.h, r.y, r.h, r.h, backColor)

		textScale := 0.8 * float32(r.h) / float32(baseFontHeight)
		textW, textH := window.GetScaledTextSize(text, textScale)
		window.DrawScaledText(
			text,
			r.x+(r.w-textW)/2,
			r.y+(r.h-textH)/2,
			textScale,
			textColor,
		)

		if r.contains(mouseX, mouseY) {
			r.expand(10).fill(window, hoverColor)
			if leftClick {
				state.toggleButton(state.lastReplayedFrame, button)
			}
		}
	}

	const (
		startButtonW           = abButtonSize
		startButtonH           = abButtonSize / 3
		startSelectButtonDistX = startButtonH / 2
	)
	selectButtonX := inputMenuX + (inputMenuW-2*startButtonW-startSelectButtonDistX)/2
	startButtonX := selectButtonX + startButtonW + startSelectButtonDistX
	startButtonY := dpadY + 4*dpadButtonSize
	startButtonRect := rect(startButtonX, startButtonY, startButtonW, startButtonH)
	selectButtonRect := rect(selectButtonX, startButtonY, startButtonW, startButtonH)

	drawStartSelect(startButtonRect, "Start", ButtonStart)
	drawStartSelect(selectButtonRect, "sElect", ButtonSelect)
}

func wasLeftClicked(window draw.Window) bool {
	for _, c := range window.Clicks() {
		if c.Button == draw.LeftButton {
			return true
		}
	}
	return false
}

func (state *editorState) executeEditorFrame(window draw.Window) {
	windowW, windowH := window.Size()
	mouseX, mouseY := window.MousePosition()
	rightMouseButtonDown := window.IsMouseDown(draw.RightButton)
	leftMouseButtonDown := window.IsMouseDown(draw.LeftButton)
	leftClick := wasLeftClicked(window)
	shiftDown := window.IsKeyDown(draw.KeyLeftShift) || window.IsKeyDown(draw.KeyRightShift)
	controlDown := window.IsKeyDown(draw.KeyLeftControl) || window.IsKeyDown(draw.KeyRightControl)
	altDown := window.IsKeyDown(draw.KeyLeftAlt) || window.IsKeyDown(draw.KeyRightAlt)
	frameCountX := windowW / frameWidth
	frameCountY := windowH / frameHeight
	lastLeftMostFrame := state.leftMostFrame
	lastActiveSelection := state.activeSelection

	// Handle inputs.

	if controlDown && !state.controlWasDown {
		state.startDraggingFrameInputs(state.activeSelection.first)
	}

	if state.infoText != "" && window.WasKeyPressed(draw.KeyEscape) {
		state.resetInfoText()
		state.render()
	}

	// Append digits to the repeat counter text.
	for i := range 10 {
		if window.WasKeyPressed(draw.Key0+draw.Key(i)) ||
			window.WasKeyPressed(draw.KeyNum0+draw.Key(i)) {

			digit := strconv.Itoa(i)

			n, err := strconv.Atoi(state.infoText + digit)
			isValidNumber := err == nil && 1 <= n && n <= 216000

			if !isValidNumber {
				state.setInfo(digit)
			} else {
				state.setInfo(state.infoText + digit)
			}
			state.render()
		}
	}

	repeatCount, err := strconv.Atoi(state.infoText)
	repeatCountValid := err == nil
	repeatCount = max(repeatCount, 1)

	if state.lastAction.valid {
		newAction := state.lastAction

		if strings.ContainsAny(window.Characters(), "+p") {
			// Append input to the end.
			newAction.count += repeatCount
		}

		if strings.Contains(window.Characters(), "P") {
			// Prepend input to the start.
			newAction.count += repeatCount
			newAction.frameIndex -= repeatCount
		}

		if strings.ContainsAny(window.Characters(), "-m") {
			// Remove inputs from the end.
			newAction.count = max(1, newAction.count-repeatCount)
		}

		if strings.Contains(window.Characters(), "M") {
			delta := min(repeatCount, state.lastAction.count-1)
			newAction.frameIndex += delta
			newAction.count -= delta
		}

		if newAction != state.lastAction {
			b := state.lastAction.button
			down := state.lastAction.down

			// First undo the last action, then apply the new action.
			state.setButtonDown(state.lastAction.frameIndex, state.lastAction.count, b, !down)
			state.setButtonDown(newAction.frameIndex, newAction.count, b, down)

			state.activeSelection.first = newAction.frameIndex
			state.activeSelection.last = newAction.frameIndex + newAction.count - 1
			state.lastAction = newAction

			state.resetInfoText()
			state.render()
		}
	}

	state.keyRepeatCountdown--
	keyTriggered := func(key draw.Key) bool {
		if window.WasKeyPressed(key) || window.IsKeyDown(key) && state.keyRepeatCountdown <= 0 {
			state.keyRepeatCountdown = 8
			return true
		}
		return false
	}

	frameDelta := 0
	if keyTriggered(draw.KeyLeft) {
		frameDelta = -repeatCount
	}
	if keyTriggered(draw.KeyRight) {
		frameDelta = repeatCount
	}
	if keyTriggered(draw.KeyUp) {
		frameDelta = -frameCountX * repeatCount
	}
	if keyTriggered(draw.KeyDown) {
		frameDelta = frameCountX * repeatCount
	}
	if keyTriggered(draw.KeyPageUp) {
		frameDelta = -frameCountX * frameCountY * repeatCount
	}
	if keyTriggered(draw.KeyPageDown) {
		frameDelta = frameCountX * frameCountY * repeatCount
	}

	scrollY := window.MouseWheelY()
	if scrollY != 0 {
		ticks := -int(scrollY)

		// By default we scroll down a whole line of frames.
		// Holding Shift will scroll a single frame at a time.
		// Holding Control will scroll a whole screen full of frames at
		// a time.
		delta := ticks * frameCountX
		if shiftDown {
			delta = ticks
		} else if controlDown {
			delta = ticks * frameCountX * frameCountY
		}

		state.leftMostFrame = max(0, state.leftMostFrame+delta)
	}

	// On Enter and G we go to the frame number that was typed in. In
	// this case it is not a repeat count but an absolute frame number
	// (index + 1).
	if repeatCountValid &&
		(window.WasKeyPressed(draw.KeyG) ||
			window.WasKeyPressed(draw.KeyEnter) ||
			window.WasKeyPressed(draw.KeyNumEnter)) {
		frameDelta = -state.leftMostFrame + repeatCount
		state.resetInfoText()
		state.render()
	}

	if frameDelta != 0 {
		if shiftDown {
			// Shift+Arrow Keys expands the selection.
			state.activeSelection.last = max(0, state.activeSelection.last+frameDelta)

			if state.activeSelection.last < state.leftMostFrame {
				state.leftMostFrame += frameDelta
			}
			if state.activeSelection.last >= state.leftMostFrame+frameCountX*frameCountY {
				state.leftMostFrame += frameDelta
			}
		} else if controlDown {
			// Ctrl+Arrow Keys moves the selected frame inputs around.
			selectionOffset := state.activeSelection.first - state.dragStartSelection.first + frameDelta
			state.dragFrameInputsTo(selectionOffset, lastActiveSelection)
		} else if altDown {
			// Alt+Arrow Keys moves the selection around.
			last := len(state.frameInputs) - 1
			state.activeSelection.first = max(0, min(last, state.activeSelection.first+frameDelta))
			state.activeSelection.last = max(0, min(last, state.activeSelection.last+frameDelta))
		} else {
			// Arrow Keys alone move us through time.
			state.leftMostFrame = max(0, state.leftMostFrame+frameDelta)
		}
	}

	if window.WasKeyPressed(draw.KeyHome) {
		if shiftDown {
			state.activeSelection.last = 0
		}

		state.leftMostFrame = 0
	}

	// TODO Think about shrinking state.frameInputs at the end when it has only
	// empty inputs or maybe if it has only the default inputs?
	if window.WasKeyPressed(draw.KeyEnd) {
		if shiftDown {
			state.activeSelection.last = len(state.frameInputs) - 1
		}

		state.leftMostFrame = len(state.frameInputs) - frameCountX*frameCountY - 1
	}

	frameX := mouseX / frameWidth
	frameY := mouseY / frameHeight
	frameUnderMouse := -1
	if 0 <= frameX && frameX < frameCountX &&
		0 <= frameY && frameY < frameCountY {
		frameUnderMouse = state.leftMostFrame + frameY*frameCountX + frameX
	}

	if leftClick {
		state.doubleClickPending = time.Now().Sub(state.lastLeftClick.time).Seconds() < 0.300 &&
			abs(state.lastLeftClick.x-mouseX) < 10 &&
			abs(state.lastLeftClick.y-mouseY) < 10
		singleClick := !state.doubleClickPending

		if state.doubleClickPending {
			state.pendingDoubleClickFrame = frameUnderMouse
		}

		if singleClick && frameUnderMouse != -1 {
			if shiftDown {
				state.activeSelection.last = frameUnderMouse
			} else if controlDown {
				state.startDraggingFrameInputs(frameUnderMouse)
			} else {
				// On single-click, make the frame under the mouse active.
				state.activeSelection.first = frameUnderMouse
				state.activeSelection.last = frameUnderMouse

				state.lastLeftClick.time = time.Now()
				state.lastLeftClick.x = mouseX
				state.lastLeftClick.y = mouseY
			}
		}
	}

	if leftMouseButtonDown && frameUnderMouse != -1 {
		state.activeSelection.last = frameUnderMouse
	}

	if !leftMouseButtonDown && state.doubleClickPending {
		state.doubleClickPending = false

		if frameUnderMouse != -1 && frameUnderMouse == state.pendingDoubleClickFrame {
			// On double-click, select all frames left and right that have the
			// same button states.
			a, b := frameUnderMouse, frameUnderMouse
			for a-1 >= 0 && state.frameInputs[a-1] == state.frameInputs[a] {
				a--
			}
			for b+1 < len(state.frameInputs) && state.frameInputs[b+1] == state.frameInputs[b] {
				b++
			}
			state.activeSelection.first = a
			state.activeSelection.last = b
		}
	}

	if leftMouseButtonDown && state.dragStartFrame != -1 && frameUnderMouse != -1 {
		selectionOffset := frameUnderMouse - state.dragStartFrame
		state.dragFrameInputsTo(selectionOffset, lastActiveSelection)
	}

	if !leftMouseButtonDown {
		state.dragStartFrame = -1
	}

	// Use the right mouse button for dragging the screen around.
	if rightMouseButtonDown && frameUnderMouse != -1 {
		if state.draggingFrameIndex == -1 {
			state.draggingFrameIndex = frameUnderMouse
		} else {
			screenIndex := frameY*frameCountX + frameX
			state.leftMostFrame = state.draggingFrameIndex - screenIndex
		}
	}

	if !rightMouseButtonDown {
		state.draggingFrameIndex = -1
	}

	if state.leftMostFrame != lastLeftMostFrame ||
		state.activeSelection != lastActiveSelection {
		state.resetInfoText()
		state.render()
	}

	if window.WasKeyPressed(draw.KeyBackspace) ||
		window.WasKeyPressed(draw.KeyDelete) {
		for i := state.activeSelection.start(); i < state.activeSelection.end(); i++ {
			state.frameInputs[i] = 0
		}
		state.setDirtyFrame(state.activeSelection.start())
		state.render()
	}

	for key, b := range keyMap {
		if window.WasKeyPressed(key) {
			state.resetInfoText()

			firstFrameIndex := state.activeSelection.start()
			down := !state.isButtonDown(firstFrameIndex, b)

			singleFrameSelected := state.activeSelection.first == state.activeSelection.last

			if shiftDown && singleFrameSelected {
				// Toggle button for all the future if we do not overwrite any
				// existing future button of this kind.
				// TODO Allow toggling even though the button is pressed in the
				// future already.
				canToggle := true
				for i := firstFrameIndex + 2; i < len(state.frameInputs); i++ {
					canToggle = canToggle && state.isButtonDown(i, b) == state.isButtonDown(i-1, b)
				}

				if canToggle {
					state.setButtonDown(firstFrameIndex, len(state.frameInputs)-firstFrameIndex, b, down)
					setButtonDown(&state.defaultInputs, b, down)
				} else {
					state.setWarning("Cannot toggle button, it is already used in the future.")
				}
			} else if singleFrameSelected {
				// Toggle button for the active frame.
				for state.activeSelection.first+repeatCount > len(state.frameInputs) {
					state.frameInputs = append(state.frameInputs, state.defaultInputs)
				}
				state.setButtonDown(state.activeSelection.first, repeatCount, b, down)

				state.lastAction = inputAction{
					valid:      true,
					frameIndex: state.activeSelection.first,
					button:     b,
					down:       down,
					count:      repeatCount,
				}

				state.activeSelection.first = state.lastAction.frameIndex
				state.activeSelection.last = state.lastAction.frameIndex + state.lastAction.count - 1
			} else {
				// We have multiple frames selected.
				state.setButtonDown(state.activeSelection.start(), state.activeSelection.count(), b, down)
				state.lastAction = inputAction{
					valid:      true,
					frameIndex: state.activeSelection.start(),
					button:     b,
					down:       down,
					count:      state.activeSelection.count(),
				}
			}

			state.render()
		}
	}

	// Render the state.

	if state.lastWindowW != windowW || state.lastWindowH != windowH {
		state.render()
	}

	if state.screenDirty {
		state.screenDirty = false

		// We need to create the Gameboy screens for these frames:
		// [leftMostFrame..lastVisibleFrame]
		lastVisibleFrame := state.leftMostFrame + frameCountX*frameCountY - 1

		// TODO Remember these until we change frames.
		state.screenBuffer = state.screenBuffer[:0]
		for i := state.leftMostFrame; i <= lastVisibleFrame; i++ {
			gb := state.generateFrame(i)
			state.screenBuffer = append(state.screenBuffer, gb.PreparedData)
		}

		screenCount := frameCountX * frameCountY
		bytesPerScreen := ScreenWidth * ScreenHeight * 4
		screenBufferSize := screenCount * bytesPerScreen
		if cap(state.gameboyScreenBuffer) < screenBufferSize {
			state.gameboyScreenBuffer = make([]byte, screenBufferSize)
			for i := 3; i < len(state.gameboyScreenBuffer); i += 4 {
				state.gameboyScreenBuffer[i] = 255
			}
		}
		state.gameboyScreenBuffer = state.gameboyScreenBuffer[:screenBufferSize]

		bufferW := frameCountX * ScreenWidth
		bufferH := frameCountY * ScreenHeight
		for frameY := range frameCountY {
			for frameX := range frameCountX {
				screenOffsetX := frameX * ScreenWidth
				screenOffsetY := frameY * ScreenHeight
				screen := state.screenBuffer[frameX+frameY*frameCountX]
				for y := range ScreenHeight {
					for x := range ScreenWidth {
						c := screen[x][y]
						destX := screenOffsetX + x
						destY := screenOffsetY + y
						dest := 4 * (destX + destY*bufferW)
						copy(state.gameboyScreenBuffer[dest:], c[:])
					}
				}
			}
		}

		window.CreateImage("gameboyScreens", bufferW, bufferH)
		window.SetImagePixels("gameboyScreens", state.gameboyScreenBuffer)

		frameIndex := state.leftMostFrame
		for frameY := range frameCountY {
			for frameX := range frameCountX {
				screenOffsetX := frameX*frameWidth + 1
				screenOffsetY := frameY*frameHeight + fontHeight
				inputs := state.frameInputs[frameIndex]

				// Determine color by button state for this frame.
				borderColor := draw.RGBA(0, 0, 0, 1)

				// Create a 4 bit value for the directional keys: DURL
				// (down up right left).
				var directionalButtons byte
				if isButtonDown(inputs, ButtonLeft) {
					directionalButtons += 1
				}
				if isButtonDown(inputs, ButtonRight) {
					directionalButtons += 2
				}
				if isButtonDown(inputs, ButtonUp) {
					directionalButtons += 4
				}
				if isButtonDown(inputs, ButtonDown) {
					directionalButtons += 8
				}

				// Valid combinations, which you could actually press on
				// a real Gameboy, get a green tint between 100 and 200.
				// Illegal combinations, like Left+Right, get 255 so
				// they stand out as a very bright green.
				borderColor.G = []float32{
					0,           // durl
					100 / 255.0, // durL
					157 / 255.0, // duRl
					255 / 255.0, // duRL
					114 / 255.0, // dUrl
					128 / 255.0, // dUrL
					142 / 255.0, // dURl
					255 / 255.0, // dURL
					171 / 255.0, // Durl
					200 / 255.0, // DurL
					185 / 255.0, // DuRl
					255 / 255.0, // DuRL
					255 / 255.0, // DUrl
					255 / 255.0, // DUrL
					255 / 255.0, // DURl
					255 / 255.0, // DURL
				}[directionalButtons]

				if isButtonDown(inputs, ButtonA) ||
					isButtonDown(inputs, ButtonStart) ||
					isButtonDown(inputs, ButtonSelect) {
					borderColor.B = 192 / 255.0
				}

				if isButtonDown(inputs, ButtonB) {
					borderColor.R = 192 / 255.0
				}

				// Color the frame border.
				frameLeft := frameX * frameWidth
				frameTop := frameY * frameHeight
				window.FillRect(frameLeft, frameTop, frameWidth, fontHeight, borderColor)
				window.FillRect(frameLeft, frameTop, 1, frameHeight, borderColor)
				window.FillRect(frameLeft, frameTop+frameHeight-1, frameWidth, 1, borderColor)
				window.FillRect(frameLeft+frameWidth-1, frameTop, 1, frameHeight, borderColor)

				// Render the Gameboy screen.

				window.DrawImageFilePart(
					"gameboyScreens",
					frameX*ScreenWidth, frameY*ScreenHeight, ScreenWidth, ScreenHeight,
					screenOffsetX, screenOffsetY, ScreenWidth, ScreenHeight,
					0,
				)
				isActiveFrame := state.activeSelection.start() <= frameIndex && frameIndex < state.activeSelection.end()
				if isActiveFrame {
					highlightColor := draw.RGBA(1, 0.5, 0.5, 0.2)
					window.FillRect(screenOffsetX, screenOffsetY, ScreenWidth, ScreenHeight, highlightColor)
				}

				// Render the text above the frame.
				textY := frameY * frameHeight

				topLeftText := strconv.Itoa(frameIndex)
				window.DrawScaledText(topLeftText, screenOffsetX, textY, textScale, draw.White)
				topLeftTextWidth, _ := window.GetScaledTextSize(topLeftText, textScale)

				text := ""
				add := func(b Button, pressed string) {
					if isButtonDown(inputs, b) {
						text += " " + pressed
					}
				}
				add(ButtonLeft, "<")
				add(ButtonUp, "^")
				add(ButtonRight, ">")
				add(ButtonDown, "v")
				add(ButtonA, "A")
				add(ButtonB, "B")
				add(ButtonSelect, "Sel")
				add(ButtonStart, "Start")

				textWidth, _ := window.GetScaledTextSize(text, textScale)
				textX := screenOffsetX + (topLeftTextWidth+ScreenWidth-textWidth)/2
				window.DrawScaledText(text, textX, textY, textScale, draw.White)

				frameIndex++
			}
		}

		window.FillRect(frameCountX*frameWidth, 0, windowW, windowH, draw.Black)
		window.FillRect(0, frameCountY*frameHeight, windowW, windowH, draw.Black)

		if state.infoText != "" {
			textW, textH := window.GetScaledTextSize(state.infoText, textScale)
			textX := windowW - textW
			textY := windowH - textH
			window.FillRect(textX-1, textY-1, windowW, windowH, draw.Black)
			window.DrawScaledText(state.infoText, textX, textY, textScale, state.infoTextColor)
		}
	}

	state.controlWasDown = controlDown
}

func (s *editorState) startDraggingFrameInputs(atFrame int) {
	// Start dragging frame inputs around with keyboard or mouse.
	s.dragStartFrame = atFrame
	s.dragStartSelection = s.activeSelection
	s.dragStartInputs = append(s.dragStartInputs[:0], s.frameInputs...)
}

func (state *editorState) dragFrameInputsTo(selectionOffset int, lastActiveSelection frameSelection) {
	state.activeSelection = frameSelection{
		first: max(0, state.dragStartSelection.first+selectionOffset),
		last:  max(0, state.dragStartSelection.last+selectionOffset),
	}

	if state.activeSelection == lastActiveSelection {
		// No real dragging has occurred, e.g. if the mouse cursor is still
		// inside the start frame and has only been moved one pixel.
		return
	}

	// TODO We could allow changing the last action after dragging it, in case
	// the last action is the one that was being dragged.
	state.lastAction.valid = false

	// Reset the input state to before the start of the drag.
	copy(state.frameInputs, state.dragStartInputs)
	// There might be more frame inputs than before the drag, so fill those with
	// the default input state.
	for i := len(state.dragStartInputs); i < len(state.frameInputs); i++ {
		state.frameInputs[i] = state.defaultInputs
	}

	dragStart := state.dragStartSelection.start()
	dragCount := state.dragStartSelection.count()
	dragEnd := dragStart + dragCount - 1

	newStart := state.activeSelection.start()
	newEnd := state.activeSelection.end() - 1

	var leftFill inputState
	if dragStart > 0 {
		leftFill = state.dragStartInputs[dragStart-1]
	}

	rightFill := state.defaultInputs
	if dragEnd+1 < len(state.dragStartInputs) {
		rightFill = state.dragStartInputs[dragEnd+1]
	}

	for i := range dragCount {
		src := dragStart + i
		dest := newStart + i
		if dest < len(state.frameInputs) {
			state.frameInputs[dest] = state.dragStartInputs[src]
		} else {
			state.frameInputs = append(state.frameInputs, state.dragStartInputs[src])
		}
	}

	for i := dragStart; i < newStart; i++ {
		state.frameInputs[i] = leftFill
	}
	for i := dragEnd; i > newEnd; i-- {
		state.frameInputs[i] = rightFill
	}

	state.setDirtyFrame(min(dragStart, newStart))
	state.render()
}

type mouseClick struct {
	time time.Time
	x    int
	y    int
}

type inputAction struct {
	valid      bool
	frameIndex int
	button     Button
	down       bool
	count      int
}

type gameboyScreen [ScreenWidth][ScreenHeight][3]uint8

// frameSelection has the first and last selected frame indices where first was
// selected before (in time) last. They can be in any order. If first == last
// then a single frame is selected. If first < last the selection was done
// forward in time, if first > last the selection was done backward in time.
type frameSelection struct {
	first int
	last  int
}

func (s *frameSelection) start() int {
	return min(s.first, s.last)
}

func (s *frameSelection) end() int {
	return max(s.first, s.last) + 1
}

func (s *frameSelection) count() int {
	return abs(s.first-s.last) + 1
}

func lastSessionPath() string {
	return filepath.Join(os.Getenv("APPDATA"), "gameboy.speedrun")
}

func (s *editorState) loadLastSpeedrun() {
	data, err := os.ReadFile(lastSessionPath())
	if err != nil {
		return
	}

	rest := data
	loadFailed := false
	n := func() int {
		if loadFailed || len(rest) < 4 {
			loadFailed = true
			return 0
		}
		n := binary.LittleEndian.Uint32(rest)
		rest = rest[4:]
		return int(n)
	}
	b := func() byte {
		if loadFailed || len(rest) < 1 {
			loadFailed = true
			return 0
		}
		b := rest[0]
		rest = rest[1:]
		return b
	}
	v := func(x any) {
		if !loadFailed {
			r := bytes.NewReader(rest)
			lenBeforeRead := r.Len()
			err := binary.Read(r, binary.LittleEndian, x)
			if err != nil {
				loadFailed = true
			} else {
				readCount := lenBeforeRead - r.Len()
				rest = rest[readCount:]
			}
		}
	}

	if n() != sessionFileVersion {
		// We currently only read the very lastest file version.
		return
	}
	leftMostFrameTemp := n()
	activeSelectionFirstTemp := n()
	activeSelectionLastTemp := n()
	defaultInputsTemp := inputState(b())
	frameInputsTemp := make([]inputState, n())
	for i := range frameInputsTemp {
		frameInputsTemp[i] = inputState(b())
	}

	haveKeyFrameInterval := n()
	haveGameboyStateVersion := n()
	var keyFrameStatesTemp []Gameboy
	if haveKeyFrameInterval == keyFrameInterval &&
		haveGameboyStateVersion == gameboyStateVersion {
		// The binary Gameboy state on disk might be old. We might have changed
		// the Gameboy struct. After a change we will have incremented
		// gameboyStateVersion so in that case we do NOT read the key frames
		// from disk. In that case we need to re-generate them.
		keyFrameStatesTemp = make([]Gameboy, n())
		for i := range keyFrameStatesTemp {
			v(&keyFrameStatesTemp[i])
		}
	}

	if loadFailed {
		fmt.Println("loading failed")
		return
	}

	s.leftMostFrame = leftMostFrameTemp
	s.activeSelection.first = activeSelectionFirstTemp
	s.activeSelection.last = activeSelectionLastTemp
	s.defaultInputs = defaultInputsTemp
	s.frameInputs = frameInputsTemp
	s.keyFrameStates = keyFrameStatesTemp
	s.frameCache.clear()
}

func (s *editorState) saveCurrentSpeedrun() {
	// Create a buffer and helper functions:
	// n() saves a number as uint32
	// b() saves a single byte
	// v() saves an arbitrary value.
	var buf bytes.Buffer
	var saveErr error
	setErr := func(err error) {
		if saveErr == nil {
			saveErr = err
		}
	}
	n := func(n int) {
		setErr(binary.Write(&buf, binary.LittleEndian, uint32(n)))
	}
	b := func(b byte) {
		setErr(buf.WriteByte(b))
	}
	v := func(x any) {
		setErr(binary.Write(&buf, binary.LittleEndian, x))
	}

	// Serialize the data.
	n(sessionFileVersion)
	n(s.leftMostFrame)
	n(s.activeSelection.first)
	n(s.activeSelection.last)
	b(byte(s.defaultInputs))
	n(len(s.frameInputs))
	for _, inputs := range s.frameInputs {
		b(byte(inputs))
	}
	n(keyFrameInterval)
	n(gameboyStateVersion)
	n(len(s.keyFrameStates))
	for _, s := range s.keyFrameStates {
		v(s)
	}

	setErr(os.WriteFile(lastSessionPath(), buf.Bytes(), 0666))

	if saveErr != nil {
		fmt.Println("error saving session file:", saveErr)
	}
}

func startProfiling() {
	path := time.Now().Format("profile_2006_01_02_15_04_05.prof")
	f, err := os.Create(path)
	check(err)
	check(pprof.StartCPUProfile(f))
}

func stopProfiling() {
	pprof.StopCPUProfile()
}

func getRom() ([]byte, error) {
	romPath, err := getRomPath()
	if err != nil {
		return nil, err
	}

	return os.ReadFile(romPath)
}

// Determine the ROM location. If the string in the flag value is empty then it
// should prompt the user to select a rom file using the OS dialog.
func getRomPath() (string, error) {
	rom := flag.Arg(0)
	if rom != "" {
		return rom, nil
	}
	return dialog.File().
		Title("Load GameBoy ROM File").
		Filter("GameBoy ROM", "gb", "gbc", "bin").
		Load()
}

type inputState byte

func isButtonDown(s inputState, b Button) bool {
	return s&(1<<b) != 0
}

func setButtonDown(s *inputState, b Button, down bool) {
	if down {
		*s |= 1 << b
	} else {
		*s &= ^(1 << b)
	}
}

func toggleButton(s *inputState, b Button) {
	setButtonDown(s, b, !isButtonDown(*s, b))
}

func newFrameCache() *frameCache {
	return &frameCache{}
}

const frameCacheSize = 100

type frameCache struct {
	frameIndices      []int
	gameboys          []Gameboy
	nextIndexToRemove int
}

func (c *frameCache) removeFramesStartingAt(frameIndex int) {
	n := 0
	for i := range c.frameIndices {
		if c.frameIndices[i] < frameIndex {
			c.frameIndices[n] = c.frameIndices[i]
			c.gameboys[n] = c.gameboys[i]
			n++
		}
	}
	c.frameIndices = c.frameIndices[:n]
	c.gameboys = c.gameboys[:n]
}

func (c *frameCache) clear() {
	c.frameIndices = c.frameIndices[:0]
	c.gameboys = c.gameboys[:0]
	c.nextIndexToRemove = 0
}

func (c *frameCache) contains(frameIndex int) bool {
	return slices.Contains(c.frameIndices, frameIndex)
}

func (c *frameCache) at(frameIndex int) Gameboy {
	i := slices.Index(c.frameIndices, frameIndex)
	if i == -1 {
		return Gameboy{}
	}
	return c.gameboys[i]
}

func (c *frameCache) set(frameIndex int, gb Gameboy) {
	i := slices.Index(c.frameIndices, frameIndex)
	if i != -1 {
		c.gameboys[i] = gb
	} else {
		if len(c.gameboys) < frameCacheSize {
			c.frameIndices = append(c.frameIndices, frameIndex)
			c.gameboys = append(c.gameboys, gb)
		} else {
			j := c.nextIndexToRemove
			c.frameIndices[j] = frameIndex
			c.gameboys[j] = gb
			c.nextIndexToRemove = (c.nextIndexToRemove + 1) % frameCacheSize
		}
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func round(x float64) int {
	if x < 0 {
		return int(x - 0.5)
	}
	return int(x + 0.5)
}

func toColor(rgb [3]byte) draw.Color {
	return draw.RGB(
		float32(rgb[0])/255,
		float32(rgb[1])/255,
		float32(rgb[2])/255,
	)
}

func rgb(r, g, b byte) draw.Color {
	return draw.RGB(
		float32(r)/255,
		float32(g)/255,
		float32(b)/255,
	)
}

func square(x int) int {
	return x * x
}

type rectangle struct {
	x, y, w, h int
}

func rect(x, y, w, h int) rectangle {
	return rectangle{
		x: x,
		y: y,
		w: w,
		h: h,
	}
}

func (r rectangle) contains(x, y int) bool {
	return r.x <= x && x < r.x+r.w &&
		r.y <= y && y < r.y+r.h
}

func (r rectangle) expand(by int) rectangle {
	return r.expandXY(by, by)
}

func (r rectangle) expandXY(byX, byY int) rectangle {
	return rectangle{
		x: r.x - byX,
		y: r.y - byY,
		w: r.w + 2*byX,
		h: r.h + 2*byY,
	}
}

func (r rectangle) fill(window draw.Window, color draw.Color) {
	window.FillRect(r.x, r.y, r.w, r.h, color)
}

func (r rectangle) fillEllipse(window draw.Window, color draw.Color) {
	window.FillEllipse(r.x, r.y, r.w, r.h, color)
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
