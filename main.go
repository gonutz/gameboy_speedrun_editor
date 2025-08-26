package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
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
	dmgMode    = flag.Bool("dmg", false, "set to force dmg mode")
	cpuprofile = flag.String("cpuprofile", "", "write cpu profile to file (debugging)")
	vsyncOff   = flag.Bool("disableVsync", false, "set to disable vsync (debugging)")
	unlocked   = flag.Bool("unlocked", false, "if to unlock the cpu speed (debugging)")
)

func main() {
	flag.Parse()

	romFile := getROM()

	rom, err := os.ReadFile(romFile)
	check(err)

	globalROM = rom

	const windowWidth, windowHeight = 1500, 800

	// The following variables are the actual state of the editor that gets
	// saved to and loaded from disk:

	leftMostFrame := 0

	var activeSelection frameSelection

	// frameInputs holds the state of all the Gameboy buttons for each frame.
	var frameInputs []inputState
	var defaultInputs inputState

	const keyFrameInterval = 100
	var keyFrameStates []Gameboy

	// The following variables are volatile editor state that DOES NOT get saved
	// to and loaded from disk.

	dragStartFrame := -1
	var dragStartSelection frameSelection
	var dragStartInputs []inputState
	var stateBeforeDrag struct {
		start  int
		frames []inputState
	}

	frameCache := newFrameCache()

	var gameboyScreenBuffer []byte

	doubleClickPending := false
	pendingDoubleClickFrame := -1

	controlWasDown := false

	// We can toggle between the editor which freezes time and shows multiple
	// frames at once and running the emulator which replays the game in
	// real-time using our edited inputs.
	replayingGame := false
	replayPaused := false
	nextReplayFrame := 0
	var emulatorGameboy Gameboy

	// We generate Gameboy screens to be display in our editor.
	// screenBuffer is a temporary buffer that we reuse in every frame.
	type gameboyScreen [ScreenWidth][ScreenHeight][3]uint8
	var screenBuffer []gameboyScreen

	// emulateFromIndex is the first frame that needs to be emulated again
	// because its state has changed. All frames after this (and including it)
	// need to be emulated again.
	emulateFromIndex := 0

	keyRepeatCountdown := 0
	draggingFrameIndex := -1

	var lastLeftClick struct {
		time time.Time
		x    int
		y    int
	}

	var lastAction struct {
		valid      bool
		frameIndex int
		button     Button
		down       bool
		count      int
	}

	infoText := ""
	infoTextColor := draw.RGBA(1, 1, 1, 1)

	setInfo := func(msg string) {
		infoText = msg
		infoTextColor = draw.RGBA(1, 1, 1, 1)
	}

	setWarning := func(msg string) {
		infoText = msg
		infoTextColor = draw.RGBA(1, 92/255.0, 92/255.0, 1)
	}

	resetInfoText := func() {
		infoText = ""
	}

	screenDirty := true

	render := func() {
		screenDirty = true
	}

	lastSessionPath := filepath.Join(os.Getenv("APPDATA"), "gameboy.speedrun")
	const sessionFileVersion = 1

	loadLastSpeedrun := func() {
		data, err := os.ReadFile(lastSessionPath)
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
		if keyFrameInterval != n() {
			// We currently do not support different key frame intervals.
			loadFailed = true
		}
		keyFrameStatesTemp := make([]Gameboy, n())
		for i := range keyFrameStatesTemp {
			v(&keyFrameStatesTemp[i])
		}

		if loadFailed {
			fmt.Println("loading failed")
			return
		}

		leftMostFrame = leftMostFrameTemp
		activeSelection.first = activeSelectionFirstTemp
		activeSelection.last = activeSelectionLastTemp
		defaultInputs = defaultInputsTemp
		frameInputs = frameInputsTemp
		keyFrameStates = keyFrameStatesTemp
		emulateFromIndex = len(keyFrameStates) * keyFrameInterval
	}

	saveCurrentSpeedrun := func() {
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
		n(leftMostFrame)
		n(activeSelection.first)
		n(activeSelection.last)
		b(byte(defaultInputs))
		n(len(frameInputs))
		for _, inputs := range frameInputs {
			b(byte(inputs))
		}
		n(keyFrameInterval)
		n(len(keyFrameStates))
		for _, s := range keyFrameStates {
			v(s)
		}

		setErr(os.WriteFile(lastSessionPath, buf.Bytes(), 0666))

		if saveErr != nil {
			fmt.Println("error saving session file:", saveErr)
		}
	}

	loadLastSpeedrun()
	defer saveCurrentSpeedrun()

	const textScale = 0.8
	const fontHeight = 13
	const frameWidth = 1 + ScreenWidth + 1
	const frameHeight = fontHeight + ScreenHeight + 1

	updateGameboy := func(gameboy *Gameboy, frameIndex int) {
		var inputs inputState

		if replayingGame {
			// While in game if we have used up the last inputs, we forever
			// press the default inputs.
			if frameIndex < len(frameInputs) {
				inputs = frameInputs[frameIndex]
			} else {
				inputs = defaultInputs
			}
		} else {
			// In the editor, we extend the frame inputs of we reach their end.
			for frameIndex >= len(frameInputs) {
				frameInputs = append(frameInputs, defaultInputs)
			}

			inputs = frameInputs[frameIndex]
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

	fullscreen := false
	toggleFullscreen := func(window draw.Window) {
		fullscreen = !fullscreen
		window.SetFullscreen(fullscreen)
	}

	createKeyFramesUpTo := func(keyFrameIndex int) {
		for keyFrameIndex >= len(keyFrameStates) {
			last := len(keyFrameStates) - 1

			if last == -1 {
				gb := NewGameboy(rom, GameboyOptions{})
				updateGameboy(&gb, 0)
				keyFrameStates = append(keyFrameStates, gb)
			} else {
				gb := keyFrameStates[last]
				for i := range keyFrameInterval {
					updateGameboy(&gb, last*keyFrameInterval+i+1)
				}
				keyFrameStates = append(keyFrameStates, gb)
			}
		}
	}

	initReplayFrame := func(frameIndex int) {
		frameIndex = max(0, frameIndex)

		if frameCache.contains(frameIndex) {
			emulatorGameboy = frameCache.at(frameIndex)
		} else {
			keyFrameIndex := frameIndex / keyFrameInterval
			createKeyFramesUpTo(keyFrameIndex)
			emulatorGameboy = keyFrameStates[keyFrameIndex]

			currentFrame := keyFrameIndex * keyFrameInterval
			for currentFrame < frameIndex {
				currentFrame++
				updateGameboy(&emulatorGameboy, currentFrame)
				frameCache.set(currentFrame, emulatorGameboy)
			}
		}

		emulatorGameboy.Sound.Init(!*mute)
		nextReplayFrame = frameIndex + 1
		unmuteSound()
	}

	const (
		charWidth   = 7
		charHeight  = 13
		fontDescend = 3
	)

	keyMap := map[draw.Key]Button{
		draw.KeyL: ButtonLeft,
		draw.KeyU: ButtonUp,
		draw.KeyR: ButtonRight,
		draw.KeyD: ButtonDown,
		draw.KeyA: ButtonA,
		draw.KeyB: ButtonB,
		draw.KeyS: ButtonStart,
		draw.KeyE: ButtonSelect,
	}

	lastWindowW, lastWindowH := 0, 0

	check(draw.RunWindow("Gameboy Speedrun Editor", windowWidth, windowHeight, func(window draw.Window) {
		windowW, windowH := window.Size()
		mouseX, mouseY := window.MousePosition()

		shiftDown := window.IsKeyDown(draw.KeyLeftShift) || window.IsKeyDown(draw.KeyRightShift)
		controlDown := window.IsKeyDown(draw.KeyLeftControl) || window.IsKeyDown(draw.KeyRightControl)
		altDown := window.IsKeyDown(draw.KeyLeftAlt) || window.IsKeyDown(draw.KeyRightAlt)

		toggleReplay := false
		if window.WasKeyPressed(draw.KeyEscape) {
			if replayingGame {
				toggleReplay = true
			} else if infoText != "" {
				resetInfoText()
				render()
			}
		}

		if window.WasKeyPressed(draw.KeyF11) || window.WasKeyPressed(draw.KeyF) {
			toggleFullscreen(window)
		}

		if window.WasKeyPressed(draw.KeySpace) {
			if replayingGame {
				replayPaused = !replayPaused
				if replayPaused {
					muteSound()
				} else {
					unmuteSound()
				}
			} else {
				toggleReplay = true
			}
		}

		if toggleReplay {
			replayingGame = !replayingGame

			resetInfoText()

			if replayingGame {
				frameCache.clear()
				initReplayFrame(leftMostFrame)
			} else {
				muteSound()
			}

			render()
		}

		if replayingGame {
			// Render the current screen.
			window.CreateImage("gameboyScreen", ScreenWidth, ScreenHeight)
			rgba := make([]byte, 4*ScreenWidth*ScreenHeight)
			i := 0
			for y := range ScreenHeight {
				for x := range ScreenWidth {
					color := emulatorGameboy.PreparedData[x][y]
					rgba[i+0] = color[0]
					rgba[i+1] = color[1]
					rgba[i+2] = color[2]
					rgba[i+3] = 255
					i += 4
				}
			}
			window.SetImagePixels("gameboyScreen", rgba)

			window.FillRect(0, 0, windowW, windowH, toColor(ColorPalette[3]))

			// Letterbox the Gameboy screen into our window.
			xScale := float64(windowW) / ScreenWidth
			yScale := float64(windowH) / ScreenHeight
			scale := math.Min(yScale, xScale)
			w := round(scale * ScreenWidth)
			h := round(scale * ScreenHeight)
			x := (windowW - w) / 2
			y := (windowH - h) / 2
			window.DrawImageFileTo("gameboyScreen", x, y, w, h, 0)

			// Let the user toggle buttons for the current frame.
			for key, b := range keyMap {
				if window.WasKeyPressed(key) {
					down := isButtonDown(frameInputs[nextReplayFrame], b)
					setButtonDown(&frameInputs[nextReplayFrame], b, !down)
					frameCache.clear()
					emulateFromIndex = nextReplayFrame
				}
			}

			// Emulate the next frame.
			keyRepeatCountdown--
			keyTriggered := func(key draw.Key) bool {
				if replayPaused {
					if window.WasKeyPressed(key) || window.IsKeyDown(key) && keyRepeatCountdown <= 0 {
						keyRepeatCountdown = 10
						return true
					}
					return false
				} else {
					return window.IsKeyDown(key)
				}
			}

			frameDelta := 1
			if replayPaused {
				frameDelta = 0
			}
			if window.WasKeyPressed(draw.KeyHome) {
				frameDelta = -nextReplayFrame
			} else if keyTriggered(draw.KeyLeft) {
				frameDelta = -1
			} else if keyTriggered(draw.KeyUp) {
				frameDelta = -5
			} else if keyTriggered(draw.KeyPageUp) {
				frameDelta = -20
			} else if keyTriggered(draw.KeyRight) {
				if replayPaused {
					frameDelta = 1
				} else {
					frameDelta = 2
				}
			} else if keyTriggered(draw.KeyDown) {
				frameDelta = 5
			} else if keyTriggered(draw.KeyPageDown) {
				frameDelta = 20
			}

			if frameDelta < 0 {
				initReplayFrame(nextReplayFrame - 1 + frameDelta)
			} else {
				for range frameDelta {
					updateGameboy(&emulatorGameboy, nextReplayFrame)
					nextReplayFrame++
				}
			}
		} else {
			frameCountX := windowW / frameWidth
			frameCountY := windowH / frameHeight

			// Handle inputs.

			lastLeftMostFrame := leftMostFrame
			lastActiveSelection := activeSelection

			startDraggingFrameInputs := func(atFrame int) {
				// Start dragging frame inputs around with the keyboard.
				dragStartFrame = atFrame
				dragStartSelection = activeSelection
				toCopy := frameInputs[activeSelection.start():activeSelection.end()]
				dragStartInputs = append(dragStartInputs[:0], toCopy...)
				stateBeforeDrag.frames = append(stateBeforeDrag.frames[:0], toCopy...)
				stateBeforeDrag.start = activeSelection.start()
			}

			dragFrameInputsTo := func(selectionOffset int, adjustView bool) {
				activeSelection = dragStartSelection
				activeSelection.first = max(0, activeSelection.first+selectionOffset)
				activeSelection.last = max(0, activeSelection.last+selectionOffset)

				if activeSelection != lastActiveSelection {
					// Reset the input state to before the start of the drag.
					for i := range stateBeforeDrag.frames {
						frameInputs[stateBeforeDrag.start+i] = stateBeforeDrag.frames[i]
					}

					changeStart := min(dragStartSelection.start(), activeSelection.start())
					changeEnd := max(dragStartSelection.end(), activeSelection.end())

					for changeEnd >= len(frameInputs) {
						frameInputs = append(frameInputs, defaultInputs)
					}

					stateBeforeDrag.start = changeStart
					stateBeforeDrag.frames = append(stateBeforeDrag.frames[:0], frameInputs[changeStart:changeEnd+1]...)

					var fillLeft inputState
					if dragStartSelection.start() >= 1 {
						fillLeft = frameInputs[dragStartSelection.start()-1]
					}

					fillRight := defaultInputs
					end := dragStartSelection.end()
					if end < len(frameInputs) {
						fillRight = frameInputs[end]
					}

					for i := dragStartSelection.start(); i < activeSelection.start(); i++ {
						frameInputs[i] = fillLeft
					}

					for i := activeSelection.end(); i < dragStartSelection.end(); i++ {
						frameInputs[i] = fillRight
					}

					for i := len(dragStartInputs) - 1; i >= 0; i-- {
						j := dragStartSelection.end() - 1 - i + selectionOffset
						if j >= 0 {
							frameInputs[j] = dragStartInputs[i]
						}
					}

					if adjustView {
						// Make sure the new frames are in view. Only do this if
						// less than a screen full of frames is selected. In case
						// the user hits Shift+End we do not want the view to skip
						// all the way to the end. In this case, we leave the view
						// as is.
						if activeSelection.count() < frameCountX*frameCountY {
							leftMostFrame = min(leftMostFrame, activeSelection.start())
							leftMostFrame = max(leftMostFrame, activeSelection.end()-frameCountX*frameCountY)
						}
					}

					emulateFromIndex = min(dragStartSelection.start(), activeSelection.start())
					render()
				}
			}

			if controlDown && !controlWasDown {
				startDraggingFrameInputs(activeSelection.first)
			}

			for i := range 10 {
				if window.WasKeyPressed(draw.Key0+draw.Key(i)) ||
					window.WasKeyPressed(draw.KeyNum0+draw.Key(i)) {
					_, err := strconv.Atoi(infoText)
					isNumber := err == nil
					if !isNumber {
						resetInfoText()
					}
					// At this point we have a number in infoText or infoText is
					// empty. If infoText is empty, we cannot type a zero, only
					// [1-9]. Zero can be typed only after other digits.
					if i > 0 || infoText != "" {
						setInfo(infoText + strconv.Itoa(i))
						render()
					}
				}
			}

			repeatCount, err := strconv.Atoi(infoText)
			repeatCountValid := err == nil
			repeatCount = max(repeatCount, 1)

			if lastAction.valid {
				newAction := lastAction

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
					delta := min(repeatCount, lastAction.count-1)
					newAction.frameIndex += delta
					newAction.count -= delta
				}

				if newAction != lastAction {
					// start := lastAction.frameIndex
					b := lastAction.button
					down := lastAction.down

					// First undo the last action.
					for i := range lastAction.count {
						j := lastAction.frameIndex + i
						setButtonDown(&frameInputs[j], b, !down)
					}

					// Now apply the new action.
					lastAction = newAction
					for i := range lastAction.count {
						j := lastAction.frameIndex + i
						if j >= len(frameInputs) {
							frameInputs = append(frameInputs, defaultInputs)
						}
						setButtonDown(&frameInputs[j], b, down)
					}

					activeSelection.first = lastAction.frameIndex
					activeSelection.last = lastAction.frameIndex + lastAction.count - 1

					resetInfoText()
					render()
				}
			}

			frameDelta := 0
			keyRepeatCountdown--
			keyTriggered := func(key draw.Key) bool {
				if window.WasKeyPressed(key) || window.IsKeyDown(key) && keyRepeatCountdown <= 0 {
					keyRepeatCountdown = 8
					return true
				}
				return false
			}
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
				frameDelta = ticks * frameCountX
				if shiftDown {
					frameDelta = ticks
				} else if controlDown {
					frameDelta = ticks * frameCountX * frameCountY
				}
			}

			// On Enter and G we go to the frame number that was typed in. In
			// this case it is not a repeat count but an absolute frame number
			// (index + 1).
			if repeatCountValid &&
				(window.WasKeyPressed(draw.KeyG) ||
					window.WasKeyPressed(draw.KeyEnter) ||
					window.WasKeyPressed(draw.KeyNumEnter)) {
				frameDelta = -leftMostFrame + repeatCount
				resetInfoText()
				render()
			}

			if frameDelta != 0 {
				if shiftDown && scrollY == 0 {
					activeSelection.last = max(0, activeSelection.last+frameDelta)

					if activeSelection.last < leftMostFrame {
						leftMostFrame += frameDelta
					}
					if activeSelection.last >= leftMostFrame+frameCountX*frameCountY {
						leftMostFrame += frameDelta
					}
				} else if controlDown && scrollY == 0 {
					selectionOffset := activeSelection.first - dragStartSelection.first + frameDelta
					dragFrameInputsTo(selectionOffset, true)
				} else if altDown && scrollY == 0 {
					last := len(frameInputs) - 1
					activeSelection.first = max(0, min(last, activeSelection.first+frameDelta))
					activeSelection.last = max(0, min(last, activeSelection.last+frameDelta))
				} else {
					leftMostFrame = max(0, leftMostFrame+frameDelta)
				}
			}

			if window.WasKeyPressed(draw.KeyHome) {
				if shiftDown {
					activeSelection.last = 0
				}

				leftMostFrame = 0
			}
			if window.WasKeyPressed(draw.KeyEnd) {
				if shiftDown {
					activeSelection.last = len(frameInputs) - 1
				}

				leftMostFrame = len(frameInputs) - frameCountX*frameCountY - 1
			}

			frameX := mouseX / frameWidth
			frameY := mouseY / frameHeight
			frameUnderMouse := -1
			if 0 <= frameX && frameX < frameCountX &&
				0 <= frameY && frameY < frameCountY {
				frameUnderMouse = leftMostFrame + frameY*frameCountX + frameX
			}

			leftClick := false
			for _, c := range window.Clicks() {
				leftClick = leftClick || c.Button == draw.LeftButton
			}

			if leftClick {
				doubleClickPending = time.Now().Sub(lastLeftClick.time).Seconds() < 0.300 &&
					abs(lastLeftClick.x-mouseX) < 10 &&
					abs(lastLeftClick.y-mouseY) < 10
				if doubleClickPending {
					pendingDoubleClickFrame = frameUnderMouse
				}
				singleClick := !doubleClickPending

				if singleClick {
					if frameUnderMouse != -1 {
						if shiftDown {
							activeSelection.last = frameUnderMouse
						} else if controlDown {
							startDraggingFrameInputs(frameUnderMouse)
						} else {
							// On single-click, make the frame under the mouse active.
							activeSelection.first = frameUnderMouse
							activeSelection.last = frameUnderMouse

							lastLeftClick.time = time.Now()
							lastLeftClick.x = mouseX
							lastLeftClick.y = mouseY
						}
					}
				}
			}

			leftMouseButtonDown := window.IsMouseDown(draw.LeftButton)

			if leftMouseButtonDown && frameUnderMouse != -1 {
				activeSelection.last = frameUnderMouse
			}

			if !leftMouseButtonDown && doubleClickPending {
				doubleClickPending = false

				if frameUnderMouse != -1 && frameUnderMouse == pendingDoubleClickFrame {
					// On double-click, select all frames left and right that
					// have the same button states.
					a, b := frameUnderMouse, frameUnderMouse
					for a-1 >= 0 && frameInputs[a-1] == frameInputs[a] {
						a--
					}
					for b+1 < len(frameInputs) && frameInputs[b+1] == frameInputs[b] {
						b++
					}
					activeSelection.first = a
					activeSelection.last = b
					render()
				}
			}

			if leftMouseButtonDown && dragStartFrame != -1 && frameUnderMouse != -1 {
				selectionOffset := frameUnderMouse - dragStartFrame
				dragFrameInputsTo(selectionOffset, false)
			}

			if !leftMouseButtonDown {
				dragStartFrame = -1
			}

			rightMouseButtonDown := window.IsMouseDown(draw.RightButton)

			// Use the right mouse button for dragging the screen around.
			if rightMouseButtonDown {
				frameX := mouseX / frameWidth
				frameY := mouseY / frameHeight

				if 0 <= frameX && frameX < frameCountX &&
					0 <= frameY && frameY < frameCountY {
					draggingFrameIndex = leftMostFrame + frameY*frameCountX + frameX
				}
			}

			if draggingFrameIndex != -1 && rightMouseButtonDown {
				frameX := mouseX / frameWidth
				frameY := mouseY / frameHeight

				if 0 <= frameX && frameX < frameCountX &&
					0 <= frameY && frameY < frameCountY {
					screenIndex := frameY*frameCountX + frameX
					leftMostFrame = draggingFrameIndex - screenIndex
				}
			}

			if !rightMouseButtonDown {
				draggingFrameIndex = -1
			}

			if leftMostFrame != lastLeftMostFrame ||
				activeSelection != lastActiveSelection {
				resetInfoText()
				render()
			}

			if window.WasKeyPressed(draw.KeyBackspace) ||
				window.WasKeyPressed(draw.KeyDelete) {
				for i := activeSelection.start(); i < activeSelection.end(); i++ {
					frameInputs[i] = 0
				}
				render()
			}

			for key, b := range keyMap {
				if window.WasKeyPressed(key) {
					resetInfoText()

					firstFrameIndex := activeSelection.start()
					down := !isButtonDown(frameInputs[firstFrameIndex], b)

					if shiftDown && activeSelection.first == activeSelection.last {
						// Toggle button for all the future if we do not
						// overwrite any existing future button of this kind.
						canToggle := true
						for i := firstFrameIndex + 2; i < len(frameInputs); i++ {
							canToggle = canToggle &&
								isButtonDown(frameInputs[i], b) == isButtonDown(frameInputs[i-1], b)
						}

						if canToggle {
							for i := firstFrameIndex; i < len(frameInputs); i++ {
								setButtonDown(&frameInputs[i], b, down)
							}
							setButtonDown(&defaultInputs, b, down)
						} else {
							setWarning("Cannot toggle button, it is already used in the future.")
						}
					} else if activeSelection.first == activeSelection.last {
						// Toggle button for the active frame.
						for activeSelection.first+repeatCount > len(frameInputs) {
							frameInputs = append(frameInputs, defaultInputs)
						}
						for i := range repeatCount {
							setButtonDown(&frameInputs[activeSelection.first+i], b, down)
						}

						lastAction.valid = true
						lastAction.frameIndex = activeSelection.first
						lastAction.button = b
						lastAction.down = down
						lastAction.count = repeatCount

						activeSelection.first = lastAction.frameIndex
						activeSelection.last = lastAction.frameIndex + lastAction.count - 1
					} else {
						// We have multiple frames selected.
						for i := activeSelection.start(); i < activeSelection.end(); i++ {
							setButtonDown(&frameInputs[i], b, down)
						}
						lastAction.valid = true
						lastAction.frameIndex = activeSelection.start()
						lastAction.button = b
						lastAction.down = down
						lastAction.count = activeSelection.count()
					}

					emulateFromIndex = firstFrameIndex
					render()
				}
			}

			// Render the state.

			if lastWindowW != windowW || lastWindowH != windowH {
				render()
			}

			emulateFrames := func(startFrame, endFrame int) []gameboyScreen {
				screenBuffer = screenBuffer[:0]

				keyFrameIndex := startFrame / keyFrameInterval

				// When inputs change we need to re-generate key frames after the
				// change. We do this by simply removing those key frames from the
				// array. They will be re-generated after that.
				lastValidKeyFrame := (emulateFromIndex + keyFrameInterval - 1) / keyFrameInterval
				lastValidKeyFrame = min(lastValidKeyFrame, len(keyFrameStates))
				keyFrameStates = keyFrameStates[:lastValidKeyFrame]

				createKeyFramesUpTo(keyFrameIndex)

				emulateFromIndex = len(keyFrameStates) * keyFrameInterval

				gb := keyFrameStates[keyFrameIndex]

				// TODO Think about exactly (especially considering the frame
				// selection) the generation of key frames and input states
				// expands. This code will generate one more frame input than
				// frames visible on the screen. When we move a selection past
				// the last frame, we use >= instead of > which was a hack to
				// work around this extra frame.
				currentFrame := keyFrameIndex * keyFrameInterval
				for currentFrame < startFrame {
					currentFrame++
					updateGameboy(&gb, currentFrame)
				}

				for currentFrame <= endFrame {
					screenBuffer = append(screenBuffer, gb.PreparedData)
					currentFrame++
					updateGameboy(&gb, currentFrame)
				}

				return screenBuffer
			}

			if screenDirty {
				startTime := time.Now() // TODO Remove timing when we are fast.
				screenDirty = false

				// We need to create the Gameboy screens for these frames:
				// [leftMostFrame..lastVisibleFrame]
				lastVisibleFrame := leftMostFrame + frameCountX*frameCountY - 1
				// TODO Remember these until we change frames.
				screens := emulateFrames(leftMostFrame, lastVisibleFrame)

				screenCount := frameCountX * frameCountY
				bytesPerScreen := ScreenWidth * ScreenHeight * 4
				screenBufferSize := screenCount * bytesPerScreen
				if cap(gameboyScreenBuffer) < screenBufferSize {
					gameboyScreenBuffer = make([]byte, screenBufferSize)
					for i := 3; i < len(gameboyScreenBuffer); i += 4 {
						gameboyScreenBuffer[i] = 255
					}
				}
				gameboyScreenBuffer = gameboyScreenBuffer[:screenBufferSize]

				bufferW := frameCountX * ScreenWidth
				bufferH := frameCountY * ScreenHeight
				for frameY := range frameCountY {
					for frameX := range frameCountX {
						screenOffsetX := frameX * ScreenWidth
						screenOffsetY := frameY * ScreenHeight
						screen := screens[frameX+frameY*frameCountX]
						for y := range ScreenHeight {
							for x := range ScreenWidth {
								c := screen[x][y]
								destX := screenOffsetX + x
								destY := screenOffsetY + y
								dest := 4 * (destX + destY*bufferW)
								copy(gameboyScreenBuffer[dest:], c[:])
							}
						}
					}
				}

				window.CreateImage("gameboyScreens", bufferW, bufferH)
				window.SetImagePixels("gameboyScreens", gameboyScreenBuffer)

				frameIndex := leftMostFrame
				for frameY := range frameCountY {
					for frameX := range frameCountX {
						screenOffsetX := frameX*frameWidth + 1
						screenOffsetY := frameY*frameHeight + fontHeight
						inputs := frameInputs[frameIndex]

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
						isActiveFrame := activeSelection.start() <= frameIndex && frameIndex < activeSelection.end()
						if isActiveFrame {
							highlightColor := draw.RGBA(1, 0.5, 0.5, 0.2)
							window.FillRect(screenOffsetX, screenOffsetY, ScreenWidth, ScreenHeight, highlightColor)
						}

						// Render the text above the frame.
						textY := frameY * frameHeight

						topLeftText := strconv.Itoa(frameIndex)
						window.DrawScaledText(topLeftText, screenOffsetX, textY, textScale, draw.White)
						topLeftTextWidth := len(topLeftText) * charWidth

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

						textWidth := len(text) * charWidth
						textX := screenOffsetX + (topLeftTextWidth+ScreenWidth-textWidth)/2
						window.DrawScaledText(text, textX, textY, textScale, draw.White)

						frameIndex++
					}
				}

				window.FillRect(frameCountX*frameWidth, 0, windowW, windowH, draw.Black)
				window.FillRect(0, frameCountY*frameHeight, windowW, windowH, draw.Black)

				if infoText != "" {
					textW, textH := window.GetScaledTextSize(infoText, textScale)
					textX := windowW - textW
					textY := windowH - textH
					window.FillRect(textX-1, textY-1, windowW, windowH, draw.Black)
					window.DrawScaledText(infoText, textX, textY, textScale, infoTextColor)
				}

				fmt.Println(time.Now().Sub(startTime))
			}

			controlWasDown = controlDown
		}

		lastWindowW, lastWindowH = windowW, windowH
	}))
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

func saveScreenshot(gameboy *Gameboy, path string) {
	f, err := os.Create(path)
	check(err)
	defer f.Close()

	rgba := image.NewRGBA(image.Rect(0, 0, ScreenWidth, ScreenHeight))

	for y := range ScreenHeight {
		for x := range ScreenWidth {
			c := gameboy.PreparedData[x][y]
			rgba.SetRGBA(x, y, color.RGBA{R: c[0], G: c[1], B: c[2], A: 255})
		}
	}

	png.Encode(f, rgba)
}

// frameSelection has the first and last selected frame indices where first was
// selected before (in time) last. They can be in any order. If first == last
// then a single frame is selected. If first < last the selection was done
// forward in time, if first > last the selection was done backward in time.
type frameSelection struct {
	first int
	last  int

	// TODO Have this?
	// forever bool
	// Or have count be a special value?
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

func newFrameCache() *frameCache {
	return &frameCache{}
}

const frameCacheSize = 100

type frameCache struct {
	frameIndices      []int
	gameboys          []Gameboy
	nextIndexToRemove int
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

// Determine the ROM location. If the string in the flag value is empty then it
// should prompt the user to select a rom file using the OS dialog.
func getROM() string {
	rom := flag.Arg(0)
	if rom == "" {
		var err error
		rom, err = dialog.File().
			Title("Load GameBoy ROM File").
			Filter("GameBoy ROM", "zip", "gb", "gbc", "bin").
			Load()
		if err != nil {
			panic(err)
		}
	}
	return rom
}

// Start the CPU profile to a the file passed in from the flag.
func startCPUProfiling() {
	log.Print("Starting CPU profile...")
	f, err := os.Create(*cpuprofile)
	if err != nil {
		log.Fatalf("Failed to create CPU profile: %v", err)
	}
	err = pprof.StartCPUProfile(f)
	if err != nil {
		log.Fatalf("Failed to start CPU profile: %v", err)
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

func check(err error) {
	if err != nil {
		panic(err)
	}
}
