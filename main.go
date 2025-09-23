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
	"unicode"
	"unicode/utf8"

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
	windowTitle = "Gameboy Speedrun Editor"

	keyFrameInterval      = 100
	minSessionFileVersion = 1
	sessionFileVersion    = 4

	baseTextScale  = 0.8
	baseFontHeight = 13

	infoTextScale = 2 * baseTextScale

	inputMenuW       = 220
	inputMenuMargin  = 20
	hoverMargin      = 10
	frameNumberScale = 1.9
	abButtonSize     = 75

	abButtonSpaceX         = abButtonSize / 8
	dpadButtonSize         = abButtonSize * 7 / 10
	startButtonW           = abButtonSize
	startButtonH           = abButtonSize / 3
	startSelectButtonDistX = startButtonH / 2
)

var scalePercentages = []int{
	50,
	55,
	60,
	65,
	70,
	75,
	80,
	85,
	90,
	95,
	100,
	110,
	120,
	130,
	140,
	155,
	170,
	185,
	200,
	220,
	240,
	260,
	285,
	300,
	335,
	370,
	400,
	450,
	500,
	550,
	600,
	700,
	800,
}

func bestFitScale(destScale float64) float64 {
	best := 1.0
	for _, percent := range scalePercentages {
		scale := float64(percent) / 100
		if math.Abs(destScale-scale) < math.Abs(destScale-best) {
			best = scale
		}
	}
	return best
}

func main() {
	flag.Parse()

	if *cpuprofile {
		startProfiling()
		defer stopProfiling()
	}

	state := newEditorState()
	state.loadLastSpeedrun()
	defer state.saveCurrentSpeedrun()

	if len(globalROM) == 0 {
		var err error
		globalROM, err = getRom()
		check(err)
	}

	check(draw.RunWindow(windowTitle, 1540, 800, func(window draw.Window) {
		windowW, windowH := window.Size()
		defer func() {
			state.lastWindowW, state.lastWindowH = windowW, windowH
		}()

		if state.isModalDialogOpen {
			state.executeModalDialogFrame(window)
		} else {
			state.executeMainFrame(window)
		}
	}))
}

func (state *editorState) executeModalDialogFrame(window draw.Window) {
	if state.replayingGame {
		state.executeReplayFrame(newReadOnlyWindow(window))
	} else {
		state.executeEditorFrame(newReadOnlyWindow(window))
	}

	for _, r := range window.Characters() {
		if r == '\b' {
			// Backspace deletes the last character.
			_, size := utf8.DecodeLastRuneInString(state.dialogText)
			state.dialogText = state.dialogText[:len(state.dialogText)-size]
		} else if r == 127 {
			// Control + Backspace deletes the last word.
			letters := []rune(state.dialogText)
			end := len(letters)
			for end > 0 && letters[end-1] == ' ' {
				end--
			}
			for end > 0 && letters[end-1] != ' ' {
				end--
			}
			state.dialogText = string(letters[:end])
		} else if r == 27 {
			// Escape cancels the dialog.
			state.cancelBranchRenameDialog()
		} else if r == '\r' {
			// Enter accepts the dialog.
			state.acceptBranchRenameDialog()
		} else if unicode.IsGraphic(r) {
			// Non-control characters get appended to the text.
			state.dialogText += string(r)
		}
	}

	windowW, windowH := window.Size()
	dialogW, dialogH := 500, 200
	dialogX := (windowW - dialogW) / 2
	dialogY := (windowH - dialogH) / 2

	dialogR := rect(dialogX, dialogY, dialogW, dialogH)

	dialogR.fill(window, draw.Black)
	dialogR.inset(5).fill(window, draw.White)

	const textScale = 2

	title := "Enter new Branch Name"
	titleW, titleH := window.GetScaledTextSize(title, textScale)
	titleX := dialogX + (dialogW-titleW)/2
	titleY := dialogY + dialogH/2 - titleH - 10
	window.DrawScaledText(title, titleX, titleY, textScale, draw.Black)

	textR := rect(dialogX+30, dialogY+dialogH/2+10, dialogW-60, titleH+10)
	textR.fill(window, draw.Black)
	textR.inset(3).fill(window, draw.White)

	clip := textR.inset(5)
	window.SetClipRect(clip.x, clip.y, clip.w, clip.h)
	text := state.dialogText
	if time.Now().Unix()%2 == 0 {
		text += "|"
	}
	textW, _ := window.GetScaledTextSize(state.dialogText+"|", textScale)
	// Draw the text left-aligned except if it gets longer than the rectangle,
	// then draw it right-aligned so we can see the end of the text.
	textX := clip.x - max(0, textW-clip.w)
	window.DrawScaledText(text, textX, clip.y, textScale, draw.Black)
}

func (state *editorState) executeMainFrame(window draw.Window) {
	if window.WasKeyPressed(draw.KeyF11) || window.WasKeyPressed(draw.KeyF) {
		state.fullscreen = !state.fullscreen
		window.SetFullscreen(state.fullscreen)
	}

	controlDown := window.IsKeyDown(draw.KeyLeftControl) || window.IsKeyDown(draw.KeyRightControl)

	// When saving/loading a file, we return from the current frame,
	// otherwise the last event from the dialog (like pressing Escape) will
	// be forwarded to our editor. The one exception is the double-click.
	// See the comment on waitForLeftMouseRelease.
	if controlDown && window.WasKeyPressed(draw.KeyN) {
		err := state.createNewSpeedrun()
		if err != nil {
			state.setWarning(err.Error())
			state.render()
		} else {
			window.SetTitle(windowTitle)
		}
		state.render()
		state.waitForLeftMouseRelease = true
		return
	}
	if controlDown && window.WasKeyPressed(draw.KeyS) {
		err := state.saveFile()
		if err != nil {
			state.setWarning(err.Error())
			state.render()
		}
		state.waitForLeftMouseRelease = true
		return
	}
	if controlDown && window.WasKeyPressed(draw.KeyO) {
		path, err := state.openFile()
		if err != nil {
			state.setWarning(err.Error())
		} else {
			window.SetTitle(windowTitle + " - " + path)
		}
		state.render()
		state.waitForLeftMouseRelease = true
		return
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

		state.lastReplayedFrame = state.leftMostFrame
		state.render()
	}

	if state.replayingGame {
		state.executeReplayFrame(window)
	} else {
		state.executeEditorFrame(window)
	}
}

func newEditorState() *editorState {
	return &editorState{
		branches:                make([]branch, 1),
		scaleFactor:             1,
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
	branches        []branch
	branchIndex     int
	// keyFrameStates are the states at every keyFrameInterval-th frame. The
	// very first item in keyFrameStates is for frame 0.
	keyFrameStates []Gameboy
	scaleFactor    float64

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
	// waitForLeftMouseRelease is a hack to fix an issue after opening a load or
	// save dialog. Double clicking a file in those dialogs will trigger on the
	// second time the mouse button goes down. It will thus still be down when
	// we get back to our editor. This means window.IsMouseDown(draw.LeftButton)
	// will be true after that double-click, resulting in an unwanted selection
	// in the editor. This flag tells the editor to wait for a mouse up event
	// before accepting a new mouse down event.
	waitForLeftMouseRelease bool

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
	isModalDialogOpen bool

	infoText      string
	infoTextColor draw.Color
	dialogText    string
}

type branch struct {
	name          string
	frameInputs   []inputState // Holds the state of all the Gameboy buttons for each frame.
	defaultInputs inputState   // Button states for future frames that are not yet generated.
}

func (s *editorState) branch() *branch {
	return &s.branches[s.branchIndex]
}

func (s *editorState) inputsAt(frameIndex int) inputState {
	s.createInputsUpTo(frameIndex)
	return s.branch().frameInputs[frameIndex]
}

func (s *editorState) createInputsUpTo(frameIndex int) {
	b := s.branch()
	for frameIndex >= len(b.frameInputs) {
		b.frameInputs = append(b.frameInputs, b.defaultInputs)
	}
}

func (s *editorState) resetForNewGame() {
	s.leftMostFrame = 0
	s.activeSelection = frameSelection{}
	for i := range s.branches {
		b := &s.branches[i]
		b.frameInputs = b.frameInputs[:0]
		b.defaultInputs = 0
	}
	s.branches = s.branches[:1]
	s.keyFrameStates = s.keyFrameStates[:0]
	s.frameCache.clear()
	s.gameboyScreenBuffer = s.gameboyScreenBuffer[:0]
	s.screenBuffer = s.screenBuffer[:0]
	s.screenDirty = true
	s.dragStartFrame = -1
	s.dragStartSelection = frameSelection{}
	s.dragStartInputs = s.dragStartInputs[:0]
	s.doubleClickPending = false
	s.pendingDoubleClickFrame = -1
	s.controlWasDown = false
	s.keyRepeatCountdown = 0
	s.draggingFrameIndex = -1
	s.lastLeftClick = mouseClick{}
	s.lastAction = inputAction{}
	s.replayingGame = false
	s.replayPaused = false
	s.lastReplayPaused = false
	s.lastReplayedFrame = -1
	s.infoText = ""
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
	inputs := s.inputsAt(frameIndex)

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
	// There are three possible scenarios:
	//
	// 1. No frame up to frameIndex is cached, so we have to go from the latest
	//    key frame.
	// 2. A frame at or shortly before frameIndex is cached, so we go from that
	//    emulating that cached frame forwards.
	// 3. A frame before frameIndex is cached, BUT there is a key frame that
	//    comes after that. In this case we use the key frame because it will
	//    take fewer emulation steps than using the older cached frame.

	// Calculate latestKeyFrame, the latest frame that exists in the key frames
	// array.
	latestKeyFrameIndex := min(frameIndex/keyFrameInterval, len(s.keyFrameStates)-1)
	latestKeyFrame := latestKeyFrameIndex * keyFrameInterval

	gb, currentIndex := s.frameCache.latestFrameUpTo(frameIndex)

	if currentIndex != -1 && currentIndex >= latestKeyFrame {
		// Scenario 2: emulate forward from the cached frame.
		for currentIndex < frameIndex {
			currentIndex++
			s.updateGameboy(&gb, currentIndex)
			s.frameCache.set(currentIndex, gb)
			if currentIndex%keyFrameInterval == 0 &&
				currentIndex/keyFrameInterval == len(s.keyFrameStates) {
				s.keyFrameStates = append(s.keyFrameStates, gb)
			}
		}
		return gb
	}

	// Scenarios 1 and 3: emulate forward from the latest key frame before
	// frameIndex.
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
	gb = s.keyFrameStates[keyFrameIndex]

	// Emulate frames until we reach our destination.
	currentIndex = keyFrameIndex * keyFrameInterval
	s.frameCache.set(currentIndex, gb)

	for currentIndex < frameIndex {
		s.updateGameboy(&gb, currentIndex+1)
		currentIndex++
		s.frameCache.set(currentIndex, gb)
		if currentIndex%keyFrameInterval == 0 &&
			currentIndex/keyFrameInterval == len(s.keyFrameStates) {
			s.keyFrameStates = append(s.keyFrameStates, gb)
		}
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

func (s *editorState) setInputsRange(firstFrameIndex, lastFrameIndex int, setTo inputState) {
	s.createInputsUpTo(lastFrameIndex)

	b := s.branch()
	for i := firstFrameIndex; i <= lastFrameIndex; i++ {
		b.frameInputs[i] = setTo
	}

	s.setDirtyFrame(firstFrameIndex)
}

func (s *editorState) toggleButton(frameIndex int, button Button) {
	s.createInputsUpTo(frameIndex)
	toggleButton(&s.branch().frameInputs[frameIndex], button)
	s.setDirtyFrame(frameIndex)
}

func (s *editorState) isButtonDown(frameIndex int, button Button) bool {
	return isButtonDown(s.inputsAt(frameIndex), button)
}

func (s *editorState) setButtonDown(frameIndex, count int, button Button, down bool) {
	s.createInputsUpTo(frameIndex + count - 1)

	b := s.branch()
	for i := range count {
		setButtonDown(&b.frameInputs[frameIndex+i], button, down)
	}

	s.setDirtyFrame(frameIndex)
}

func (state *editorState) executeReplayFrame(window draw.Window) {
	windowW, windowH := window.Size()

	if window.WasKeyPressed(draw.KeySpace) {
		state.replayPaused = !state.replayPaused
		if state.replayPaused {
			muteSound()
		} else {
			unmuteSound()
		}
	}

	if window.WasKeyPressed(draw.KeyF3) {
		state.checkFrames(state.lastReplayedFrame)
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
	inputs := state.inputsAt(state.lastReplayedFrame)
	inputMenuX := screenX + screenW + inputMenuMargin
	frameNumber := fmt.Sprintf("Frame %d", state.lastReplayedFrame)
	buttonCallback := func(button Button) {
		state.toggleButton(state.lastReplayedFrame, button)
	}
	state.renderMenu(window, inputs, inputMenuX, frameNumber, buttonCallback)
}

func (state *editorState) renderMenu(
	window draw.Window,
	inputs inputState,
	inputMenuX int,
	frameNumber string,
	buttonCallback func(button Button),
) {
	_, windowH := window.Size()
	mouseX, mouseY := window.MousePosition()
	leftClick := wasLeftClicked(window)

	_, baseFontHeight := window.GetTextSize("|")
	hoverColor := draw.RGBA(0, 0.5, 0, 0.3)

	// Clear the menu background.
	window.FillRect(inputMenuX, 0, inputMenuW, windowH, rgb(224, 248, 208))

	frameNumberW, frameNumberH := window.GetScaledTextSize(frameNumber, frameNumberScale)
	frameNumberX := inputMenuX + (inputMenuW-frameNumberW)/2
	window.DrawScaledText(frameNumber, frameNumberX, 0, frameNumberScale, draw.Black)

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
				buttonCallback(button)
			}
		}
	}

	bButtonX := inputMenuX + (inputMenuW-(abButtonSize+abButtonSpaceX+abButtonSize))/2
	aButtonX := bButtonX + abButtonSize + abButtonSpaceX
	aButtonY := frameNumberH * 3 / 2
	bButtonY := aButtonY + abButtonSize/2

	drawAB(rect(aButtonX, aButtonY, abButtonSize, abButtonSize), "A", ButtonA)
	drawAB(rect(bButtonX, bButtonY, abButtonSize, abButtonSize), "B", ButtonB)

	// Draw the D-Pad.
	dpadX := inputMenuX + (inputMenuW-3*dpadButtonSize)/2
	dpadY := bButtonY + abButtonSize/2 + dpadButtonSize
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
				buttonCallback(button)
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
				buttonCallback(button)
			}
		}
	}

	selectButtonX := inputMenuX + (inputMenuW-2*startButtonW-startSelectButtonDistX)/2
	startButtonX := selectButtonX + startButtonW + startSelectButtonDistX
	startButtonY := dpadY + 3*dpadButtonSize + dpadButtonSize/2
	startButtonRect := rect(startButtonX, startButtonY, startButtonW, startButtonH)
	selectButtonRect := rect(selectButtonX, startButtonY, startButtonW, startButtonH)

	drawStartSelect(startButtonRect, "Start", ButtonStart)
	drawStartSelect(selectButtonRect, "Select", ButtonSelect)

	// Draw the branch menu.
	const menuTextScale = 1.5

	y := selectButtonRect.y + selectButtonRect.h + 10

	button := func(text string) bool {
		textW, textH := window.GetScaledTextSize(text, menuTextScale)
		newBranchButton := rect(0, y, textW+20, textH+10)
		newBranchButton.x = inputMenuX + (inputMenuW-newBranchButton.w)/2
		color := draw.LightPurple
		if newBranchButton.contains(mouseX, mouseY) {
			color = draw.Purple
		}
		newBranchButton.fill(window, color)
		textX := newBranchButton.x + (newBranchButton.w-textW)/2
		textY := newBranchButton.y + (newBranchButton.h-textH)/2
		window.DrawScaledText(text, textX, textY, menuTextScale, draw.Black)

		y += newBranchButton.h + 2

		return leftClick && newBranchButton.contains(mouseX, mouseY)
	}

	if button("New Branch") {
		b := state.branch()
		state.branches = append(state.branches, branch{
			name:          fmt.Sprintf("Branch %d", len(state.branches)+1),
			frameInputs:   slices.Clone(b.frameInputs),
			defaultInputs: b.defaultInputs,
		})
		state.branchIndex = len(state.branches) - 1
	}

	if button("Rename Branch") {
		state.startModalBranchRenameDialog(window)
	}

	if len(state.branches) > 1 && button("Delete Branch") {
		skipConfirmation := false

		// If the current branch is an exact copy of another branch, we delete
		// it without asking confirmation because no progress is really lost.
		for i := range state.branches {
			if i != state.branchIndex &&
				equalInputs(state.branches[i], state.branches[state.branchIndex]) {
				skipConfirmation = true
			}
		}

		msg := fmt.Sprintf("Do you really want to delete \"%s\"?", state.branch().name)

		if skipConfirmation || dialog.Message(msg).YesNo() {
			state.branches = slices.Delete(state.branches, state.branchIndex, state.branchIndex+1)
			state.branchIndex = max(0, state.branchIndex-1)
		}
	}

	for i, b := range state.branches {
		name := b.name
		if i == state.branchIndex {
			name = ">" + name + "<"
		}
		textW, textH := window.GetScaledTextSize(name, menuTextScale)
		textX := inputMenuX + (inputMenuW-textW)/2
		color := draw.Black
		r := rect(textX, y, textW, textH)
		if r.contains(mouseX, mouseY) {
			color = draw.Gray
		}
		window.DrawScaledText(name, textX, y, menuTextScale, color)
		y += textH

		if i != state.branchIndex && leftClick && r.contains(mouseX, mouseY) {
			oldBranch := state.branch()
			state.branchIndex = i
			newBranch := state.branch()

			end := min(
				len(oldBranch.frameInputs),
				len(newBranch.frameInputs),
			)
			dirty := end
			for i := range end {
				if oldBranch.frameInputs[i] != newBranch.frameInputs[i] {
					dirty = i
					break
				}
			}

			state.setDirtyFrame(dirty)
			state.render()
		}
	}
}

func (s *editorState) startModalBranchRenameDialog(window draw.Window) {
	s.isModalDialogOpen = true
	s.dialogText = s.branch().name
}

func (s *editorState) acceptBranchRenameDialog() {
	s.branch().name = s.dialogText
	s.cancelBranchRenameDialog()
}

func (s *editorState) cancelBranchRenameDialog() {
	s.isModalDialogOpen = false
	s.dialogText = ""
	s.render()
}

func equalInputs(a, b branch) bool {
	if len(a.frameInputs) != len(b.frameInputs) {
		return false
	}
	for i := range a.frameInputs {
		if a.frameInputs[i] != b.frameInputs[i] {
			return false
		}
	}
	return true
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

	leftDown := window.IsMouseDown(draw.LeftButton)
	state.waitForLeftMouseRelease = state.waitForLeftMouseRelease && leftDown
	leftMouseButtonDown := leftDown && !state.waitForLeftMouseRelease

	leftClick := wasLeftClicked(window)
	shiftDown := window.IsKeyDown(draw.KeyLeftShift) || window.IsKeyDown(draw.KeyRightShift)
	controlDown := window.IsKeyDown(draw.KeyLeftControl) || window.IsKeyDown(draw.KeyRightControl)
	altDown := window.IsKeyDown(draw.KeyLeftAlt) || window.IsKeyDown(draw.KeyRightAlt)
	inputMenuX := windowW - inputMenuW - inputMenuMargin
	lastLeftMostFrame := state.leftMostFrame
	lastActiveSelection := state.activeSelection

	// Handle inputs.

	if window.WasKeyPressed(draw.KeyF3) {
		state.checkFrames(state.leftMostFrame)
	}

	oldScaleFactor := bestFitScale(state.scaleFactor)

	zeroDown := window.WasKeyPressed(draw.Key0) || window.WasKeyPressed(draw.KeyNum0)
	if controlDown && zeroDown {
		state.scaleFactor = 1
	}

	if controlDown && window.WasKeyPressed(draw.KeyNumAdd) {
		state.scaleFactor = min(8, max(0.5, state.scaleFactor*1.0905))
	}
	if controlDown && window.WasKeyPressed(draw.KeyNumSubtract) {
		state.scaleFactor = min(8, max(0.5, state.scaleFactor/1.0905))
	}

	scrollY := window.MouseWheelY()
	if controlDown && scrollY != 0 {
		// We use the control key for zooming.
		state.scaleFactor = min(8, max(0.5, state.scaleFactor*math.Pow(1.0905, scrollY)))
	}

	scaleFactor := bestFitScale(state.scaleFactor)

	if scaleFactor != oldScaleFactor {
		state.setInfo(fmt.Sprintf("Zoom: %.0f%%", scaleFactor*100))
		state.render()
	}

	textScale := float32(scaleFactor * baseTextScale)
	fontHeight := round(scaleFactor * baseFontHeight)
	screenWidth := round(scaleFactor * ScreenWidth)
	screenHeight := round(scaleFactor * ScreenHeight)
	frameWidth := 1 + screenWidth + 1
	frameHeight := fontHeight + screenHeight + 1

	integerScaleUp := scaleFactor > 0 && screenWidth%ScreenWidth == 0
	window.BlurImages(!integerScaleUp)

	frameCountX := inputMenuX / frameWidth
	frameCountY := windowH / frameHeight

	if controlDown && !state.controlWasDown {
		state.startDraggingFrameInputs(state.activeSelection.first)
	}

	if state.infoText != "" && window.WasKeyPressed(draw.KeyEscape) {
		state.resetInfoText()
		state.render()
	}

	// Append digits to the repeat counter text.
	if !controlDown {
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

	if scrollY != 0 && !controlDown {
		ticks := -int(scrollY)

		// By default we scroll down a whole line of frames.
		// Holding Shift will scroll a single frame at a time.
		// Holding Control will scroll a whole screen full of frames at
		// a time.
		delta := ticks * frameCountX
		if shiftDown {
			delta = ticks
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
			last := len(state.branch().frameInputs) - 1
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
		} else {
			state.leftMostFrame = 0
		}
	}

	if window.WasKeyPressed(draw.KeyEnd) {
		if shiftDown {
			state.activeSelection.last = len(state.branch().frameInputs) - 1
		} else {
			state.leftMostFrame = len(state.branch().frameInputs) - frameCountX*frameCountY - 1
		}
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
			for a-1 >= 0 && state.inputsAt(a-1) == state.inputsAt(a) {
				a--
			}
			for b+1 < len(state.branch().frameInputs) && state.inputsAt(b+1) == state.inputsAt(b) {
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
		state.setInputsRange(
			state.activeSelection.start(),
			state.activeSelection.end()-1,
			0,
		)
		state.render()
	}

	buttonWasPressed := func(button Button) {
		state.resetInfoText()

		firstFrameIndex := state.activeSelection.start()
		down := !state.isButtonDown(firstFrameIndex, button)

		singleFrameSelected := state.activeSelection.first == state.activeSelection.last

		if shiftDown && singleFrameSelected {
			// Toggle button for all the future if we do not overwrite any
			// existing future button of this kind.
			// TODO Allow toggling even though the button is pressed in the
			// future already.
			canToggle := true
			for i := firstFrameIndex + 2; i < len(state.branch().frameInputs); i++ {
				canToggle = canToggle && state.isButtonDown(i, button) == state.isButtonDown(i-1, button)
			}

			if canToggle {
				state.setButtonDown(firstFrameIndex, len(state.branch().frameInputs)-firstFrameIndex, button, down)
				setButtonDown(&state.branch().defaultInputs, button, down)
			} else {
				state.setWarning("Cannot toggle button, it is already used in the future.")
			}
		} else if singleFrameSelected {
			// Toggle button for the active frame.
			state.setButtonDown(state.activeSelection.first, repeatCount, button, down)

			state.lastAction = inputAction{
				valid:      true,
				frameIndex: state.activeSelection.first,
				button:     button,
				down:       down,
				count:      repeatCount,
			}

			state.activeSelection.first = state.lastAction.frameIndex
			state.activeSelection.last = state.lastAction.frameIndex + state.lastAction.count - 1
		} else {
			// We have multiple frames selected.
			state.setButtonDown(state.activeSelection.start(), state.activeSelection.count(), button, down)
			state.lastAction = inputAction{
				valid:      true,
				frameIndex: state.activeSelection.start(),
				button:     button,
				down:       down,
				count:      state.activeSelection.count(),
			}
		}

		state.render()
	}

	for key, b := range keyMap {
		if window.WasKeyPressed(key) {
			buttonWasPressed(b)
		}
	}

	// Render the state.

	// Render the menu first.
	state.renderMenu(
		window,
		state.inputsAt(state.activeSelection.start()),
		inputMenuX+inputMenuMargin,
		"",
		buttonWasPressed,
	)

	if state.lastWindowW != windowW || state.lastWindowH != windowH {
		state.render()
	}

	if state.screenDirty || window.NeedsReRendering() {
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
				inputs := state.inputsAt(frameIndex)

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
					screenOffsetX, screenOffsetY, screenWidth, screenHeight,
					0,
				)
				isActiveFrame := state.activeSelection.start() <= frameIndex && frameIndex < state.activeSelection.end()
				if isActiveFrame {
					highlightColor := draw.RGBA(1, 0.5, 0.5, 0.2)
					window.FillRect(screenOffsetX, screenOffsetY, screenWidth, screenHeight, highlightColor)
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
				textX := screenOffsetX + (topLeftTextWidth+screenWidth-textWidth)/2
				window.DrawScaledText(text, textX, textY, textScale, draw.White)

				frameIndex++
			}
		}

		right := frameCountX * frameWidth
		window.FillRect(right, 0, inputMenuX+inputMenuMargin-right, windowH, draw.Black)
		window.FillRect(0, frameCountY*frameHeight, inputMenuX+inputMenuMargin, windowH, draw.Black)

		if state.infoText == "" && state.activeSelection.count() > 1 {
			state.infoText = fmt.Sprintf("%d frames selected", state.activeSelection.count())
		}

		if state.infoText != "" {
			textW, textH := window.GetScaledTextSize(state.infoText, infoTextScale)
			textX := frameCountX*frameWidth - textW
			textY := windowH - textH
			window.FillRect(textX-1, textY-1, textW+2, textH+2, draw.RGBA(0, 0, 0, 0.8))
			window.DrawScaledText(state.infoText, textX, textY, infoTextScale, state.infoTextColor)
		}
	}

	state.controlWasDown = controlDown
}

func (s *editorState) startDraggingFrameInputs(atFrame int) {
	// Start dragging frame inputs around with keyboard or mouse.
	s.dragStartFrame = atFrame
	s.dragStartSelection = s.activeSelection
	s.dragStartInputs = append(s.dragStartInputs[:0], s.branch().frameInputs...)
}

func (state *editorState) dragFrameInputsTo(selectionOffset int, lastActiveSelection frameSelection) {
	// affectedFrame might be the left-most frame to be marked dirty. If we drag
	// inputs around to the past, the back to the future, this affectedFrame is
	// the earliest frame from where we move back to the future.
	affectedFrame := state.activeSelection.start()

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

	branch := state.branch()

	// Reset the input state to before the start of the drag.
	copy(branch.frameInputs, state.dragStartInputs)
	// There might be more frame inputs than before the drag, so fill those with
	// the default input state.
	for i := len(state.dragStartInputs); i < len(branch.frameInputs); i++ {
		branch.frameInputs[i] = branch.defaultInputs
	}

	dragStart := state.dragStartSelection.start()
	dragCount := state.dragStartSelection.count()
	dragEnd := dragStart + dragCount - 1

	newStart := state.activeSelection.start()
	newEnd := state.activeSelection.end() - 1

	state.createInputsUpTo(max(dragEnd, newEnd))

	var leftFill inputState
	if dragStart > 0 {
		leftFill = state.dragStartInputs[dragStart-1]
	}

	rightFill := branch.defaultInputs
	if dragEnd+1 < len(state.dragStartInputs) {
		rightFill = state.dragStartInputs[dragEnd+1]
	}

	for i := range dragCount {
		src := dragStart + i
		dest := newStart + i
		branch.frameInputs[dest] = state.dragStartInputs[src]
	}

	for i := dragStart; i < newStart; i++ {
		branch.frameInputs[i] = leftFill
	}
	for i := dragEnd; i > newEnd; i-- {
		branch.frameInputs[i] = rightFill
	}

	state.setDirtyFrame(min(dragStart, newStart, affectedFrame))
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

func (s *editorState) createNewSpeedrun() error {
	path, err := dialog.File().
		Title("Load GameBoy ROM File").
		Filter("GameBoy ROM", "gb", "gbc", "bin", "speedrun").
		Load()

	if err != nil {
		// User cancelled the dialog.
		return nil
	}

	if strings.HasSuffix(strings.ToLower(path), ".speedrun") {
		// Load game from a speedrun file. This has to be a file version that
		// includes the game.
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		if len(data) < 8 {
			return fmt.Errorf("invalid speedrun file (too short)")
		}

		fileVersion := binary.LittleEndian.Uint32(data)
		if fileVersion < 2 {
			return fmt.Errorf("speedrun file version does not contain Gameboy ROM")
		}

		romSize := binary.LittleEndian.Uint32(data[4:])
		if len(data) < int(8+romSize) {
			return fmt.Errorf("corrupt speedrun file (incomplete Gameboy ROM)")
		}

		globalROM = slices.Clone(data[8 : 8+romSize])
	} else {
		// Load a Gameboy ROM.
		rom, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		globalROM = rom
	}

	s.resetForNewGame()
	return nil
}

func (s *editorState) openFile() (string, error) {
	path, err := dialog.File().
		Title("Load Speedrun").
		Filter("GameBoy Speedrun", "speedrun").
		Load()

	if err != nil {
		// User cancelled the dialog.
		return "", nil
	}

	err = s.open(path)
	if err != nil {
		return "", fmt.Errorf("failed to load '%s': %w", path, err)
	}

	return path, nil
}

func (state *editorState) open(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	rest := data
	var loadErr error
	n := func() int {
		if loadErr != nil {
			return 0
		}
		if len(rest) < 4 {
			loadErr = fmt.Errorf("short read: only %d bytes left trying to read a 4 byte integer", len(rest))
			return 0
		}
		n := binary.LittleEndian.Uint32(rest)
		rest = rest[4:]
		return int(n)
	}
	b := func() byte {
		if loadErr != nil {
			return 0
		}
		if len(rest) < 1 {
			loadErr = fmt.Errorf("short read: no bytes left trying to read a single byte")
			return 0
		}
		b := rest[0]
		rest = rest[1:]
		return b
	}
	s := func() string {
		length := n()
		if loadErr != nil {
			return ""
		}
		if length > len(rest) {
			loadErr = fmt.Errorf("short read: string is longer than remaining bytes")
			return ""
		}
		str := string(rest[:length])
		rest = rest[length:]
		return str
	}
	f := func() float32 {
		if loadErr != nil {
			return 0
		}
		if len(rest) < 4 {
			loadErr = fmt.Errorf("short read: float32 needs 4 bytes")
			return 0
		}
		n := binary.LittleEndian.Uint32(rest)
		rest = rest[4:]
		return math.Float32frombits(n)
	}
	v := func(x any) {
		if loadErr != nil {
			return
		}

		r := bytes.NewReader(rest)
		lenBeforeRead := r.Len()
		err := binary.Read(r, binary.LittleEndian, x)
		if err != nil {
			loadErr = err
		} else {
			readCount := lenBeforeRead - r.Len()
			rest = rest[readCount:]
		}
	}

	fileVersion := n()

	if !(minSessionFileVersion <= fileVersion && fileVersion <= sessionFileVersion) {
		if minSessionFileVersion == sessionFileVersion {
			return fmt.Errorf(
				"unsupport file version, found %d but only support version %d",
				fileVersion,
				sessionFileVersion,
			)
		}
		return fmt.Errorf(
			"unsupport file version, found %d but only support versions %d to %d",
			fileVersion,
			minSessionFileVersion,
			sessionFileVersion,
		)
	}

	if fileVersion >= 2 {
		romSize := n()
		globalROM = make([]byte, romSize)
		v(globalROM)
	}

	leftMostFrameTemp := n()
	activeSelectionFirstTemp := n()
	activeSelectionLastTemp := n()

	scaleFactorTemp := 1.0
	if fileVersion >= 4 {
		scaleFactorTemp = float64(f())
	}

	branchIndexTemp := 0
	var branchesTemp []branch
	if fileVersion < 3 {
		// There were no branches, so we map the frame inputs to a single
		// branch.
		branchesTemp = make([]branch, 1)
		branch := &branchesTemp[0]
		branch.name = "Branch 1"
		branch.defaultInputs = inputState(b())
		branch.frameInputs = make([]inputState, n())
		for i := range branch.frameInputs {
			branch.frameInputs[i] = inputState(b())
		}
	} else {
		// This version supports multiple branches.
		branchIndexTemp = n()
		branchesTemp = make([]branch, n())
		for i := range branchesTemp {
			branch := &branchesTemp[i]
			branch.name = s()
			branch.defaultInputs = inputState(b())
			branch.frameInputs = make([]inputState, n())
			for i := range branch.frameInputs {
				branch.frameInputs[i] = inputState(b())
			}
		}
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

	if !(0 <= branchIndexTemp && branchIndexTemp < len(branchesTemp)) {
		loadErr = fmt.Errorf(
			"invalid branch index %d %d branches exist",
			branchIndexTemp, len(branchesTemp),
		)
	}

	if loadErr != nil {
		return loadErr
	}

	state.leftMostFrame = leftMostFrameTemp
	state.activeSelection.first = activeSelectionFirstTemp
	state.activeSelection.last = activeSelectionLastTemp
	state.scaleFactor = scaleFactorTemp
	state.branchIndex = branchIndexTemp
	state.branches = branchesTemp
	state.keyFrameStates = keyFrameStatesTemp

	state.frameCache.clear()
	state.dragStartFrame = -1
	state.doubleClickPending = false
	state.controlWasDown = false
	state.keyRepeatCountdown = 0
	state.draggingFrameIndex = -1
	state.lastLeftClick = mouseClick{}
	state.lastAction = inputAction{}
	state.replayingGame = false
	state.replayPaused = false
	state.infoText = ""

	return nil
}

func (s *editorState) loadLastSpeedrun() {
	err := s.open(lastSessionPath())
	if err != nil {
		fmt.Println("loading last session failed:", err)
	}
}

func (s *editorState) saveFile() error {
	path, err := dialog.File().
		Title("Save Speedrun").
		Filter("GameBoy Speedrun", "speedrun").
		Save()

	if err != nil {
		// User cancelled the dialog.
		return nil
	}

	if !strings.HasSuffix(strings.ToLower(path), ".speedrun") {
		path += ".speedrun"
	}

	err = s.save(path)
	if err != nil {
		return fmt.Errorf("failed to save '%s': %w", path, err)
	}
	return nil
}

func (state *editorState) save(path string) error {
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
	s := func(s string) {
		bytes := []byte(s)
		n(len(bytes))
		_, err := buf.Write(bytes)
		setErr(err)
	}
	f := func(x float32) {
		n := math.Float32bits(x)
		setErr(binary.Write(&buf, binary.LittleEndian, n))
	}
	v := func(x any) {
		setErr(binary.Write(&buf, binary.LittleEndian, x))
	}

	// Serialize the data.
	n(sessionFileVersion)
	n(len(globalROM))
	v(globalROM)
	n(state.leftMostFrame)
	n(state.activeSelection.first)
	n(state.activeSelection.last)
	f(float32(state.scaleFactor))
	n(state.branchIndex)
	n(len(state.branches))
	for i := range state.branches {
		branch := &state.branches[i]
		s(branch.name)
		b(byte(branch.defaultInputs))
		n(len(branch.frameInputs))
		for _, inputs := range branch.frameInputs {
			b(byte(inputs))
		}
	}
	n(keyFrameInterval)
	n(gameboyStateVersion)
	n(len(state.keyFrameStates))
	for _, frame := range state.keyFrameStates {
		v(frame)
	}

	if saveErr == nil {
		setErr(os.WriteFile(path, buf.Bytes(), 0666))
	}

	return saveErr
}

func (s *editorState) saveCurrentSpeedrun() {
	err := s.save(lastSessionPath())
	if err != nil {
		fmt.Println("saving current session failed:", err)
	}
}

func (state *editorState) checkFrames(upTo int) {
	// TODO Remove debug code from final product.

	fmt.Println("checking states up to frame", upTo)

	branch := state.branch()

	wantGB := NewGameboy(globalROM, GameboyOptions{})
	for i := range upTo + 1 {
		inputs := branch.frameInputs[i]

		for b := range buttonCount {
			if isButtonDown(inputs, b) {
				wantGB.PressButton(b)
			} else {
				wantGB.ReleaseButton(b)
			}
		}

		wantGB.Update()
	}

	haveGB := state.generateFrame(upTo)

	var have, want bytes.Buffer
	binary.Write(&have, binary.LittleEndian, &haveGB)
	binary.Write(&want, binary.LittleEndian, &wantGB)
	if !bytes.Equal(have.Bytes(), want.Bytes()) {
		panic("Gameboys are not equal")
	}

	fmt.Println("no problems encountered")
	state.setInfo("no problems encountered")
	state.render()
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

const frameCacheSize = 500

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

// latestFrameUpTo returns the cached frame whose frame index is the maximum
// index <= the given frameIndex, i.e. if frameIndex is cached, the result will
// be the Gameboy at frameIndex and frameIndex; if the frame right before that
// is cached, it will be the Gameboy right before frameIndex and frameIndex-1,
// and so on.
func (c *frameCache) latestFrameUpTo(frameIndex int) (Gameboy, int) {
	bestIndex := -1
	bestFrameIndex := -1

	for i, haveIndex := range c.frameIndices {
		if haveIndex <= frameIndex && haveIndex > bestFrameIndex {
			bestIndex = i
			bestFrameIndex = haveIndex
		}
	}

	if bestIndex == -1 {
		return Gameboy{}, -1
	}

	return c.gameboys[bestIndex], c.frameIndices[bestIndex]
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

func (r rectangle) outline(window draw.Window, color draw.Color) {
	window.DrawRect(r.x, r.y, r.w, r.h, color)
}

func (r rectangle) fill(window draw.Window, color draw.Color) {
	window.FillRect(r.x, r.y, r.w, r.h, color)
}

func (r rectangle) fillEllipse(window draw.Window, color draw.Color) {
	window.FillEllipse(r.x, r.y, r.w, r.h, color)
}

func (r rectangle) inset(by int) rectangle {
	return rectangle{
		x: r.x + by,
		y: r.y + by,
		w: r.w - 2*by,
		h: r.h - 2*by,
	}
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
