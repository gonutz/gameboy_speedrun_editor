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
	activeFrameOffset := 0

	// frameInputs holds the state of all the Gameboy buttons for each frame.
	var frameInputs [][buttonCount]bool
	var defaultInputs [buttonCount]bool

	const keyFrameInterval = 100
	var keyFrameStates []Gameboy

	// The following variables are volatile editor state that DOES NOT get saved
	// to and loaded from disk.

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

	frameShiftCountdown := 0
	movingFrameIndex := -1

	bitsToInput := func(bits byte) [buttonCount]bool {
		var b [buttonCount]bool
		for i := range buttonCount {
			b[i] = bits&(1<<i) != 0
		}
		return b
	}

	inputToBits := func(inputs [buttonCount]bool) byte {
		var b byte
		for i := range buttonCount {
			if inputs[i] {
				b += 1 << i
			}
		}
		return b
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
		activeFrameOffsetTemp := n()
		defaultInputsTemp := bitsToInput(b())
		frameInputsTemp := make([][buttonCount]bool, n())
		for i := range frameInputsTemp {
			frameInputsTemp[i] = bitsToInput(b())
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
		activeFrameOffset = activeFrameOffsetTemp
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
		n(activeFrameOffset)
		b(inputToBits(defaultInputs))
		n(len(frameInputs))
		for _, inputs := range frameInputs {
			b(inputToBits(inputs))
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

	const frameWidth = 1 + ScreenWidth + 1
	const frameHeight = 13 + ScreenHeight + 1

	updateGameboy := func(gameboy *Gameboy, frameIndex int) {
		var inputs [buttonCount]bool

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
			if inputs[b] {
				gameboy.PressButton(b)
			} else {
				gameboy.ReleaseButton(b)
			}
		}

		gameboy.Update()
	}

	for !win.Closed() {
		if win.JustPressed(pixelgl.KeyEscape) {
			win.SetClosed(true)
		}

		if win.JustPressed(pixelgl.KeySpace) {
			replayingGame = !replayingGame

			if replayingGame {
				opts := GameboyOptions{
					CGBMode: !*dmgMode,
					Sound:   !*mute,
				}

				// TODO NewGameboy does not have sound, but the other way (which
				// just inlines what NewGameboy actually does) will have sound.
				// emulatorGameboy, err = NewGameboy(rom, opts)
				emulatorGameboy = Gameboy{Options: opts}
				emulatorGameboy.init(rom)

				unmuteSound()

				replayFrameIndex = 0
			} else {
				muteSound()
			}
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

			lastLeftMostFrame := leftMostFrame

			// Handle inputs.

			frameShiftCountdown--
			shiftFrames := func(key pixelgl.Button) bool {
				if win.JustPressed(key) || win.Pressed(key) && frameShiftCountdown <= 0 {
					frameShiftCountdown = 8
					return true
				}
				return false
			}
			if shiftFrames(pixelgl.KeyLeft) {
				leftMostFrame = max(0, leftMostFrame-1)
			}
			if shiftFrames(pixelgl.KeyRight) {
				leftMostFrame++
			}
			if shiftFrames(pixelgl.KeyUp) {
				leftMostFrame = max(0, leftMostFrame-10)
			}
			if shiftFrames(pixelgl.KeyDown) {
				leftMostFrame += 10
			}
			if shiftFrames(pixelgl.KeyPageUp) {
				leftMostFrame = max(0, leftMostFrame-100)
			}
			if shiftFrames(pixelgl.KeyPageDown) {
				leftMostFrame += 100
			}
			scrollDelta := win.MouseScroll()
			if scrollDelta.Y != 0 {
				leftMostFrame = max(0, leftMostFrame-int(scrollDelta.Y))
			}

			if win.JustPressed(pixelgl.KeyHome) {
				leftMostFrame = 0
			}

			needToRender := leftMostFrame != lastLeftMostFrame

			// Update the active frame (potentially).
			lastActiveFrameOffset := activeFrameOffset

			if win.JustPressed(pixelgl.MouseButton1) {
				mouse := win.MousePosition()
				mouseX := int(mouse.X)
				mouseY := canvasHeight - 1 - int(mouse.Y)
				frameX := mouseX / frameWidth
				frameY := mouseY / frameHeight

				if 0 <= frameX && frameX < frameCountX &&
					0 <= frameY && frameY < frameCountY {
					activeFrameOffset = frameY*frameCountX + frameX
					movingFrameIndex = leftMostFrame + activeFrameOffset
				}
			}

			leftButtonDown := win.Pressed(pixelgl.MouseButton1)
			if movingFrameIndex != -1 && leftButtonDown {
				mouse := win.MousePosition()
				mouseX := int(mouse.X)
				mouseY := canvasHeight - 1 - int(mouse.Y)
				frameX := mouseX / frameWidth
				frameY := mouseY / frameHeight

				if 0 <= frameX && frameX < frameCountX &&
					0 <= frameY && frameY < frameCountY {
					newActiveFrameOffset := frameY*frameCountX + frameX
					if activeFrameOffset != newActiveFrameOffset {
						leftMostFrame += activeFrameOffset - newActiveFrameOffset
						activeFrameOffset = newActiveFrameOffset
					}
				}
			}

			if !leftButtonDown {
				movingFrameIndex = -1
			}

			maxActiveFrameOffset := frameCountX*frameCountY - 1
			activeFrameOffset = min(activeFrameOffset, maxActiveFrameOffset)

			needToRender = needToRender || activeFrameOffset != lastActiveFrameOffset

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
			shiftDown := win.Pressed(pixelgl.KeyLeftShift) || win.Pressed(pixelgl.KeyRightShift)
			for key, b := range keyMap {
				if win.JustPressed(key) {
					frameIndex := leftMostFrame + activeFrameOffset
					down := !frameInputs[frameIndex][b]
					frameInputs[frameIndex][b] = down

					if shiftDown {
						defaultInputs[b] = down
						for i := frameIndex + 1; i < len(frameInputs); i++ {
							frameInputs[i][b] = down
						}
					}

					emulateFromIndex = frameIndex
					needToRender = true
				}
			}

			// Render the state.

			wantPixelLen := canvasWidth * canvasHeight * 4
			needToRecreatePixels := len(pixels) != wantPixelLen

			if needToRecreatePixels {
				pixels = make([]uint8, wantPixelLen)
				needToRender = true
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

			if needToRender {
				for i := range pixels {
					pixels[i] = 0
				}

				img := &image.RGBA{
					Pix:    pixels,
					Stride: canvasWidth * 4,
					Rect:   image.Rect(0, 0, canvasWidth, canvasHeight),
				}

				const charWidth = 7
				drawer := font.Drawer{
					Dst:  img,
					Src:  image.NewUniform(color.White),
					Face: basicfont.Face7x13,
				}

				activeFrameX := activeFrameOffset % frameCountX
				activeFrameY := activeFrameOffset / frameCountX
				activeFrameBounds := image.Rect(
					activeFrameX*frameWidth,
					activeFrameY*frameHeight,
					(activeFrameX+1)*frameWidth,
					(activeFrameY+1)*frameHeight,
				)
				border := image.NewUniform(color.RGBA{255, 0, 0, 255})
				draw.Draw(img, activeFrameBounds, border, image.Point{}, draw.Src)

				lastVisibleFrame := leftMostFrame + frameCountX*frameCountY - 1

				// We need to create the Gameboy screens for these frames:
				// [leftMostFrame..lastVisibleFrame]
				screens := emulateFrames(leftMostFrame, lastVisibleFrame)

				frameIndex := leftMostFrame
				for frameY := range frameCountY {
					for frameX := range frameCountX {
						offsetX := frameX*frameWidth + 1
						offsetY := frameY*frameHeight + 13

						screenIndex := frameIndex - leftMostFrame
						screen := screens[screenIndex]
						for y := range ScreenHeight {
							for x := range ScreenWidth {
								c := screen[x][y]
								destX := offsetX + x
								destY := offsetY + y
								dest := destX*4 + destY*canvasWidth*4
								copy(pixels[dest:], c[:])
							}
						}

						inputs := frameInputs[frameIndex]
						text := strconv.Itoa(frameIndex)
						add := func(b Button, pressed string) {
							if inputs[b] {
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
						drawer.Dot = fixed.P(offsetX+(frameWidth-textWidth)/2, offsetY-1)
						drawer.DrawString(text)

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

				pixels = invertY(pixels, canvasWidth, canvasHeight)
			}

			if len(pixels) > 0 {
				canvas.SetPixels(pixels)
			}

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

func check(err error) {
	if err != nil {
		panic(err)
	}
}
