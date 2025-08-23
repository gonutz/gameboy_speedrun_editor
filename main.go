package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/faiface/mainthread"
	"github.com/faiface/pixel"
	"github.com/faiface/pixel/pixelgl"
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
	pixelgl.Run(runEditor)
}

func runEditor() {
	romFile := getROM()

	rom, err := os.ReadFile(romFile)
	check(err)

	globalROM = rom

	const windowWidth, windowHeight = 1500, 800
	win, err := pixelgl.NewWindow(pixelgl.WindowConfig{
		Title:     "Gameboy Speedrun Editor",
		Bounds:    pixel.R(0, 0, windowWidth, windowHeight),
		VSync:     true,
		Resizable: true,
	})
	check(err)

	// Hack so that pixelgl renders on Darwin.
	win.SetPos(pixel.Vec{X: 200, Y: 150})

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

	doubleClickPending := false
	pendingDoubleClickFrame := -1

	controlWasDown := false

	// We can toggle between the editor which freezes time and shows multiple
	// frames at once and running the emulator which replays the game in
	// real-time using our edited inputs.
	replayingGame := false
	replayFrameIndex := 0
	var emulatorGameboy Gameboy
	emulatorBackbuffer := &pixel.PictureData{
		Pix:    make([]color.RGBA, ScreenWidth*ScreenHeight),
		Stride: ScreenWidth,
		Rect:   pixel.R(0, 0, ScreenWidth, ScreenHeight),
	}

	var pixels []uint8

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
	infoTextColor := color.RGBA{255, 255, 255, 255}

	setInfo := func(msg string) {
		infoText = msg
		infoTextColor = color.RGBA{255, 255, 255, 255}
	}

	setWarning := func(msg string) {
		infoText = msg
		infoTextColor = color.RGBA{255, 92, 92, 255}
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

	const fontHeight = 13
	const frameWidth = 1 + ScreenWidth + 1
	const frameHeight = fontHeight + ScreenHeight + 1

	updateGameboy := func(gameboy *Gameboy, frameIndex int) {
		var inputs inputState

		if replayingGame {
			if frameIndex < len(frameInputs) {
				inputs = frameInputs[frameIndex]
			} else {
				inputs = defaultInputs
			}
		} else {
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

	toggleFullscreen := func() {
		if win.Monitor() == nil {
			monitor := pixelgl.PrimaryMonitor()
			_, height := monitor.Size()
			win.SetMonitor(monitor)
			PixelScale = height / 144
		} else {
			win.SetMonitor(nil)
			PixelScale = 3
		}
	}

	const (
		charWidth   = 7
		charHeight  = 13
		fontDescend = 3
	)
	textRenderer := font.Drawer{
		Face: basicfont.Face7x13,
	}

	for !win.Closed() {
		toggleReplay := false

		if win.JustPressed(pixelgl.KeyEscape) {
			if replayingGame {
				toggleReplay = true
			} else if infoText != "" {
				resetInfoText()
				render()
			}
		}

		if win.JustPressed(pixelgl.KeyF11) || win.JustPressed(pixelgl.KeyF) {
			toggleFullscreen()
		}

		if win.JustPressed(pixelgl.KeySpace) {
			toggleReplay = true
		}

		if toggleReplay {
			replayingGame = !replayingGame

			resetInfoText()

			if replayingGame {
				start := max(0, min(leftMostFrame/keyFrameInterval-1, len(keyFrameStates)-1))
				emulatorGameboy = keyFrameStates[start]
				emulatorGameboy.Sound.Init(!*mute)
				replayFrameIndex = start*keyFrameInterval + 1
				unmuteSound()
			} else {
				muteSound()
			}

			render()
		}

		if replayingGame {
			updateGameboy(&emulatorGameboy, replayFrameIndex)
			replayFrameIndex++

			for y := range ScreenHeight {
				for x := range ScreenWidth {
					col := emulatorGameboy.PreparedData[x][y]
					rgb := color.RGBA{R: col[0], G: col[1], B: col[2], A: 0xFF}
					emulatorBackbuffer.Pix[(ScreenHeight-1-y)*ScreenWidth+x] = rgb
				}
			}

			col := ColorPalette[3]
			bg := color.RGBA{R: col[0], G: col[1], B: col[2], A: 0xFF}
			win.Clear(bg)

			spr := pixel.NewSprite(pixel.Picture(emulatorBackbuffer), pixel.R(0, 0, ScreenWidth, ScreenHeight))
			spr.Draw(win, pixel.IM)

			// Letterbox the emulator into our window.
			xScale := win.Bounds().W() / 160
			yScale := win.Bounds().H() / 144
			scale := math.Min(yScale, xScale)

			shift := win.Bounds().Size().Scaled(0.5).Sub(pixel.ZV)
			cam := pixel.IM.Scaled(pixel.ZV, scale).Moved(shift)
			win.SetMatrix(cam)

			win.Update()
		} else {
			canvas := win.Canvas()
			canvasSize := canvas.Bounds().Size()
			canvasWidth, canvasHeight := int(canvasSize.X), int(canvasSize.Y)

			frameCountX := canvasWidth / frameWidth
			frameCountY := canvasHeight / frameHeight

			// Handle inputs.

			lastLeftMostFrame := leftMostFrame
			lastActiveFrames := activeSelection

			shiftDown := win.Pressed(pixelgl.KeyLeftShift) || win.Pressed(pixelgl.KeyRightShift)
			controlDown := win.Pressed(pixelgl.KeyLeftControl) || win.Pressed(pixelgl.KeyRightControl)

			startDraggingFrameInputs := func(atFrame int) {
				// Start dragging frame inputs around with the keyboard.
				dragStartFrame = atFrame
				dragStartSelection = activeSelection
				toCopy := frameInputs[activeSelection.start():activeSelection.end()]
				dragStartInputs = append(dragStartInputs[:0], toCopy...)
				stateBeforeDrag.frames = append(stateBeforeDrag.frames[:0], toCopy...)
				stateBeforeDrag.start = activeSelection.start()
			}

			dragFrameInputsTo := func(selectionOffset int) {
				activeSelection = dragStartSelection
				activeSelection.first = max(0, activeSelection.first+selectionOffset)
				activeSelection.last = max(0, activeSelection.last+selectionOffset)

				if activeSelection != lastActiveFrames {
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

					// Make sure the new frames are in view. Only do this if
					// less than a screen full of frames is selected. In case
					// the user hits Shift+End we do not want the view to skip
					// all the way to the end. In this case, we leave the view
					// as is.
					if activeSelection.count() < frameCountX*frameCountY {
						leftMostFrame = min(leftMostFrame, activeSelection.start())
						leftMostFrame = max(leftMostFrame, activeSelection.end()-frameCountX*frameCountY)
					}

					emulateFromIndex = min(dragStartSelection.start(), activeSelection.start())
					render()
				}
			}

			if controlDown && !controlWasDown {
				startDraggingFrameInputs(activeSelection.first)
			}

			for i := range 10 {
				if win.JustPressed(pixelgl.Key0+pixelgl.Button(i)) ||
					win.JustPressed(pixelgl.KeyKP0+pixelgl.Button(i)) {
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

				if strings.ContainsAny(win.Typed(), "+p") {
					// Append input to the end.
					newAction.count += repeatCount
				}

				if strings.Contains(win.Typed(), "P") {
					// Prepend input to the start.
					newAction.count += repeatCount
					newAction.frameIndex -= repeatCount
				}

				if strings.ContainsAny(win.Typed(), "-m") {
					// Remove inputs from the end.
					newAction.count = max(0, newAction.count-repeatCount)
				}

				if strings.Contains(win.Typed(), "M") {
					delta := min(repeatCount, lastAction.count)
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

					resetInfoText()
					render()
				}
			}

			frameDelta := 0
			keyRepeatCountdown--
			keyTriggered := func(key pixelgl.Button) bool {
				if win.JustPressed(key) || win.Pressed(key) && keyRepeatCountdown <= 0 {
					keyRepeatCountdown = 8
					return true
				}
				return false
			}
			if keyTriggered(pixelgl.KeyLeft) {
				frameDelta = -repeatCount
			}
			if keyTriggered(pixelgl.KeyRight) {
				frameDelta = repeatCount
			}
			if keyTriggered(pixelgl.KeyUp) {
				frameDelta = -frameCountX * repeatCount
			}
			if keyTriggered(pixelgl.KeyDown) {
				frameDelta = frameCountX * repeatCount
			}
			if keyTriggered(pixelgl.KeyPageUp) {
				frameDelta = -frameCountX * frameCountY * repeatCount
			}
			if keyTriggered(pixelgl.KeyPageDown) {
				frameDelta = frameCountX * frameCountY * repeatCount
			}

			scrollDelta := win.MouseScroll()
			if scrollDelta.Y != 0 {
				ticks := -int(scrollDelta.Y)
				frameDelta = ticks * frameCountX
				if shiftDown {
					frameDelta = ticks
				} else if controlDown {
					frameDelta = ticks * frameCountX * frameCountY
				}
			}

			if repeatCountValid &&
				(win.JustPressed(pixelgl.KeyG) ||
					win.JustPressed(pixelgl.KeyEnter) ||
					win.JustPressed(pixelgl.KeyKPEnter)) {
				frameDelta = -leftMostFrame + repeatCount
				resetInfoText()
				render()
			}

			if frameDelta != 0 {
				if shiftDown && scrollDelta.Y == 0 {
					activeSelection.last = max(0, activeSelection.last+frameDelta)

					if activeSelection.last < leftMostFrame {
						leftMostFrame += frameDelta
					}
					if activeSelection.last >= leftMostFrame+frameCountX*frameCountY {
						leftMostFrame += frameDelta
					}
				} else if controlDown && scrollDelta.Y == 0 {
					selectionOffset := activeSelection.first - dragStartSelection.first + frameDelta
					dragFrameInputsTo(selectionOffset)
				} else {
					leftMostFrame = max(0, leftMostFrame+frameDelta)
				}
			}

			if win.JustPressed(pixelgl.KeyHome) {
				if shiftDown {
					activeSelection.last = 0
				}

				leftMostFrame = 0
			}
			if win.JustPressed(pixelgl.KeyEnd) {
				if shiftDown {
					activeSelection.last = len(frameInputs) - 1
				}

				leftMostFrame = len(frameInputs) - frameCountX*frameCountY - 1
			}

			mouse := win.MousePosition()
			mouseX := int(mouse.X)
			mouseY := canvasHeight - 1 - int(mouse.Y)

			frameX := mouseX / frameWidth
			frameY := mouseY / frameHeight
			frameUnderMouse := -1
			if 0 <= frameX && frameX < frameCountX &&
				0 <= frameY && frameY < frameCountY {
				frameUnderMouse = leftMostFrame + frameY*frameCountX + frameX
			}

			if win.JustPressed(pixelgl.MouseButton1) {
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

			leftMouseButtonDown := win.Pressed(pixelgl.MouseButton1)

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
				dragFrameInputsTo(selectionOffset)
			}

			if !leftMouseButtonDown {
				dragStartFrame = -1
			}

			// Use the right mouse button for dragging the screen around.
			if win.JustPressed(pixelgl.MouseButton2) {
				mouse := win.MousePosition()
				mouseX := int(mouse.X)
				mouseY := canvasHeight - 1 - int(mouse.Y)
				frameX := mouseX / frameWidth
				frameY := mouseY / frameHeight

				if 0 <= frameX && frameX < frameCountX &&
					0 <= frameY && frameY < frameCountY {
					draggingFrameIndex = leftMostFrame + frameY*frameCountX + frameX
				}
			}

			rightMouseButtonDown := win.Pressed(pixelgl.MouseButton2)

			if draggingFrameIndex != -1 && rightMouseButtonDown {
				mouse := win.MousePosition()
				mouseX := int(mouse.X)
				mouseY := canvasHeight - 1 - int(mouse.Y)
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
				activeSelection != lastActiveFrames {
				resetInfoText()
				render()
			}

			keyMap := map[pixelgl.Button]Button{
				pixelgl.KeyL: ButtonLeft,
				pixelgl.KeyU: ButtonUp,
				pixelgl.KeyR: ButtonRight,
				pixelgl.KeyD: ButtonDown,
				pixelgl.KeyA: ButtonA,
				pixelgl.KeyB: ButtonB,
				pixelgl.KeyS: ButtonStart,
				pixelgl.KeyE: ButtonSelect,
			}

			if win.JustPressed(pixelgl.KeyBackspace) ||
				win.JustPressed(pixelgl.KeyDelete) {
				for i := activeSelection.start(); i < activeSelection.end(); i++ {
					frameInputs[i] = 0
				}
				render()
			}

			for key, b := range keyMap {
				if win.JustPressed(key) {
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

			wantPixelLen := canvasWidth * canvasHeight * 4
			needToRecreatePixels := len(pixels) != wantPixelLen

			if needToRecreatePixels {
				pixels = make([]uint8, wantPixelLen)
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

				// Create new keyframes if necessary.
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
				screenDirty = false

				for i := range pixels {
					pixels[i] = 0
				}

				img := &image.RGBA{
					Pix:    pixels,
					Stride: canvasWidth * 4,
					Rect:   image.Rect(0, 0, canvasWidth, canvasHeight),
				}

				textRenderer.Dst = img
				textRenderer.Src = image.NewUniform(color.White)

				// We need to create the Gameboy screens for these frames:
				// [leftMostFrame..lastVisibleFrame]
				lastVisibleFrame := leftMostFrame + frameCountX*frameCountY - 1
				screens := emulateFrames(leftMostFrame, lastVisibleFrame)

				frameIndex := leftMostFrame
				for frameY := range frameCountY {
					for frameX := range frameCountX {
						screenOffsetX := frameX*frameWidth + 1
						screenOffsetY := frameY*frameHeight + 13
						inputs := frameInputs[frameIndex]

						// Render the frame border.
						frameBounds := image.Rect(
							frameX*frameWidth,
							frameY*frameHeight,
							(frameX+1)*frameWidth,
							(frameY+1)*frameHeight,
						)

						// Determine color by button state for this frame.
						borderColor := color.RGBA{0, 0, 0, 255}

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
						borderColor.G = []byte{
							0,   // durl
							100, // durL
							157, // duRl
							255, // duRL
							114, // dUrl
							128, // dUrL
							142, // dURl
							255, // dURL
							171, // Durl
							200, // DurL
							185, // DuRl
							255, // DuRL
							255, // DUrl
							255, // DUrL
							255, // DURl
							255, // DURL
						}[directionalButtons]

						if isButtonDown(inputs, ButtonA) ||
							isButtonDown(inputs, ButtonStart) ||
							isButtonDown(inputs, ButtonSelect) {
							borderColor.B = 192
						}

						if isButtonDown(inputs, ButtonB) {
							borderColor.R = 192
						}

						frameTop := image.Rect(
							frameBounds.Min.X,
							frameBounds.Min.Y,
							frameBounds.Max.X,
							frameBounds.Min.Y+fontHeight,
						)
						frameLeft := image.Rect(
							frameBounds.Min.X,
							frameTop.Max.Y,
							frameBounds.Min.X+1,
							frameBounds.Max.Y,
						)
						frameBottom := image.Rect(
							frameBounds.Min.X+1,
							frameTop.Max.Y,
							frameBounds.Max.X-1,
							frameBounds.Max.Y,
						)
						frameRight := image.Rect(
							frameBounds.Max.X-1,
							frameTop.Max.Y,
							frameBounds.Max.X,
							frameBounds.Max.Y,
						)
						border := image.NewUniform(borderColor)
						draw.Draw(img, frameTop, border, image.Point{}, draw.Src)
						draw.Draw(img, frameLeft, border, image.Point{}, draw.Src)
						draw.Draw(img, frameBottom, border, image.Point{}, draw.Src)
						draw.Draw(img, frameRight, border, image.Point{}, draw.Src)

						// Render the Gameboy screen.
						isActiveFrame := activeSelection.start() <= frameIndex && frameIndex < activeSelection.end()
						screenIndex := frameIndex - leftMostFrame
						screen := screens[screenIndex]
						for y := range ScreenHeight {
							for x := range ScreenWidth {
								c := screen[x][y]
								if isActiveFrame {
									// Make the active frame brighter.
									c[0] = byte(min(255, int(c[0])+60))
									c[1] = byte(min(255, int(c[1])+10))
									c[2] = byte(min(255, int(c[2])+10))
								}
								destX := screenOffsetX + x
								destY := screenOffsetY + y
								dest := destX*4 + destY*canvasWidth*4
								copy(pixels[dest:], c[:])
							}
						}

						// Render the text above the frame.
						text := strconv.Itoa(frameIndex)
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
						textRenderer.Dot = fixed.P(screenOffsetX+(frameWidth-textWidth)/2, screenOffsetY-1)
						textRenderer.DrawString(text)

						frameIndex++
					}
				}

				filledLeftPixels := frameCountX * frameWidth
				filledTopPixels := frameCountY * frameHeight

				rightEdge := image.Rect(filledLeftPixels, 0, canvasWidth, filledTopPixels)
				bottomEdge := image.Rect(0, filledTopPixels, canvasWidth, canvasHeight)

				background := image.NewUniform(color.Black)
				draw.Draw(img, rightEdge, background, image.Point{}, draw.Src)
				draw.Draw(img, bottomEdge, background, image.Point{}, draw.Src)

				if infoText != "" {
					infoTextWidth := len(infoText) * charWidth
					infoTextHeight := charHeight
					infoTextRect := image.Rect(
						canvasWidth-infoTextWidth-1,
						canvasHeight-infoTextHeight-1,
						canvasWidth,
						canvasHeight,
					)
					draw.Draw(img, infoTextRect, background, image.Point{}, draw.Src)

					textRenderer.Src = image.NewUniform(infoTextColor)
					textRenderer.Dot = fixed.P(infoTextRect.Min.X+1, canvasHeight-fontDescend+1)
					textRenderer.DrawString(infoText)
				}

				pixels = invertY(pixels, canvasWidth, canvasHeight)

				if len(pixels) > 0 {
					canvas.SetPixels(pixels)
				}
			}

			controlWasDown = controlDown

			win.Update()
		}
	}
}

func invertY(pixels []uint8, w, h int) []uint8 {
	bytesPerLine := w * 4
	swapBuffer := make([]uint8, bytesPerLine)

	for y := range h / 2 {
		topLine := pixels[y*bytesPerLine : (y+1)*bytesPerLine]
		yBottom := h - 1 - y
		bottomLine := pixels[yBottom*bytesPerLine : (yBottom+1)*bytesPerLine]
		copy(swapBuffer, topLine)
		copy(topLine, bottomLine)
		copy(bottomLine, swapBuffer)
	}

	return pixels
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

func start() {
	// Create the monitor for pixels
	monitor := NewPixelsIOBinding(*vsyncOff || *unlocked)

	// Load the rom from the flag argument, or prompt with file select
	romFile := getROM()
	rom, err := os.ReadFile(romFile)
	check(err)

	// If the CPU profile flag is set, then setup the profiling
	if *cpuprofile != "" {
		startCPUProfiling()
		defer pprof.StopCPUProfile()
	}

	opts := GameboyOptions{
		CGBMode: !*dmgMode,
		Sound:   !*mute,
	}

	// Initialise the GameBoy with the flag options
	gameboy := NewGameboy(rom, opts)
	monitor.Gameboy = &gameboy
	startGBLoop(&gameboy, monitor)
}

func startGBLoop(gameboy *Gameboy, monitor IOBinding) {
	frameTime := time.Second / FramesSecond
	if *unlocked {
		frameTime = 1
	}

	ticker := time.NewTicker(frameTime)
	start := time.Now()
	frames := 0
	for range ticker.C {
		if !monitor.IsRunning() {
			return
		}

		frames++

		monitor.ProcessInput()
		gameboy.Update()
		monitor.RenderScreen()

		since := time.Since(start)
		if since > time.Second {
			start = time.Now()
			monitor.SetTitle(frames)
			frames = 0
		}
	}
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

// IOBinding provides an interface for display and input bindings.
type IOBinding interface {
	// Init the IOBinding
	Init(disableVsync bool)
	// RenderScreen renders a frame of the game.
	RenderScreen()
	// Destroy the IOBinding instance.
	Destroy()
	// ProcessInput processes input.
	ProcessInput()
	// SetTitle sets the title of the window.
	SetTitle(fps int)
	// IsRunning returns if the monitor is still running.
	IsRunning() bool
}

// Determine the ROM location. If the string in the flag value is empty then it
// should prompt the user to select a rom file using the OS dialog.
func getROM() string {
	rom := flag.Arg(0)
	if rom == "" {
		mainthread.Call(func() {
			var err error
			rom, err = dialog.File().
				Filter("GameBoy ROM", "zip", "gb", "gbc", "bin").
				Title("Load GameBoy ROM File").Load()
			if err != nil {
				os.Exit(1)
			}
		})
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

func check(err error) {
	if err != nil {
		panic(err)
	}
}
