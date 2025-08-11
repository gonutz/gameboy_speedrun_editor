package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
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
	mute        = flag.Bool("mute", false, "mute sound output")
	dmgMode     = flag.Bool("dmg", false, "set to force dmg mode")
	cpuprofile  = flag.String("cpuprofile", "", "write cpu profile to file (debugging)")
	vsyncOff    = flag.Bool("disableVsync", false, "set to disable vsync (debugging)")
	stepThrough = flag.Bool("stepthrough", false, "step through opcodes (debugging)")
	unlocked    = flag.Bool("unlocked", false, "if to unlock the cpu speed (debugging)")
)

func main() {
	flag.Parse()
	pixelgl.Run(runEditor)
}

func runEditor() {
	rom := getROM()

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

	leftMostFrame := 0
	var pixels []uint8

	// frameInputs holds the state of all the Gameboy buttons for each frame.
	var frameInputs [][buttonCount]bool
	var defaultInputs [buttonCount]bool
	var gameboyStates []Gameboy
	// emulateFromIndex is the first frame that needs to be emulated again
	// because its state has changed. All frames after this (and including it)
	// need to be emulated again.
	emulateFromIndex := 0

	frameShiftCountdown := 0

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

	loadLastSpeedrun := func() {
		if data, err := os.ReadFile(lastSessionPath); err == nil {
			rest := data

			leftMostFrame = int(binary.LittleEndian.Uint32(rest))
			rest = rest[4:]

			defaultInputs = bitsToInput(rest[0])
			rest = rest[1:]

			frameInputs = make([][buttonCount]bool, len(rest))
			for i := range frameInputs {
				frameInputs[i] = bitsToInput(rest[i])
			}
		}
	}

	saveCurrentSpeedrun := func() {
		var buf bytes.Buffer

		binary.Write(&buf, binary.LittleEndian, uint32(leftMostFrame))

		buf.WriteByte(inputToBits(defaultInputs))

		for _, inputs := range frameInputs {
			buf.WriteByte(inputToBits(inputs))
		}

		os.WriteFile(lastSessionPath, buf.Bytes(), 0666)
	}

	loadLastSpeedrun()
	defer saveCurrentSpeedrun()

	for !win.Closed() {
		if win.JustPressed(pixelgl.KeyEscape) {
			win.SetClosed(true)
		}

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

		if win.JustPressed(pixelgl.KeyHome) {
			leftMostFrame = 0
		}

		needToRender := leftMostFrame != lastLeftMostFrame

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
				down := !frameInputs[leftMostFrame][b]
				frameInputs[leftMostFrame][b] = down

				if shiftDown {
					defaultInputs[b] = down
					for i := leftMostFrame + 1; i < len(frameInputs); i++ {
						frameInputs[i][b] = down
					}
				}

				emulateFromIndex = leftMostFrame
				needToRender = true
			}
		}

		// Render the state.

		canvas := win.Canvas()
		canvasSize := canvas.Bounds().Size()
		canvasWidth, canvasHeight := int(canvasSize.X), int(canvasSize.Y)
		wantPixelLen := canvasWidth * canvasHeight * 4

		needToRecreatePixels := len(pixels) != wantPixelLen

		if needToRecreatePixels {
			pixels = make([]uint8, canvasWidth*canvasHeight*4)
			needToRender = true
		}

		updateGameboy := func(gameboy *Gameboy, frameIndex int) {
			inputs := frameInputs[frameIndex]
			for b := range buttonCount {
				if inputs[b] {
					gameboy.PressButton(b)
				} else {
					gameboy.ReleaseButton(b)
				}
			}

			gameboy.Update()
		}

		simulateFrame := func(frameIndex int) {
			for frameIndex >= len(frameInputs) {
				frameInputs = append(frameInputs, defaultInputs)
			}

			for frameIndex >= len(gameboyStates) {
				nextFrame := len(gameboyStates)
				if nextFrame == 0 {
					gb, _ := NewGameboy(rom)
					updateGameboy(&gb, 0)
					gameboyStates = append(gameboyStates, gb)
				} else {
					gb := gameboyStates[nextFrame-1]
					updateGameboy(&gb, nextFrame)
					gameboyStates = append(gameboyStates, gb)
				}
			}

			if frameIndex >= emulateFromIndex {
				// Emulate all the obsolete states.
				for i := emulateFromIndex; i <= frameIndex; i++ {
					if i == 0 {
						gameboyStates[0], _ = NewGameboy(rom)
						updateGameboy(&gameboyStates[0], 0)
					} else {
						gameboyStates[i] = gameboyStates[i-1]
						updateGameboy(&gameboyStates[i], i)
					}
				}

				emulateFromIndex = frameIndex + 1
			}
		}

		if needToRender {
			for i := range pixels {
				pixels[i] = 0
			}

			frameWidth := 1 + ScreenWidth + 1
			frameHeight := 13 + ScreenHeight + 1

			frameCountX := canvasWidth / frameWidth
			frameCountY := canvasHeight / frameHeight

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

			frameIndex := leftMostFrame
			for frameY := range frameCountY {
				for frameX := range frameCountX {
					offsetX := frameX*frameWidth + 1
					offsetY := frameY*frameHeight + 13

					simulateFrame(frameIndex)

					for y := range ScreenHeight {
						for x := range ScreenWidth {
							// TODO Possible optimization, index
							// gameboy.PreparedData[y][x] and copy by scanline
							// instead of by pixels.
							c := gameboyStates[frameIndex].PreparedData[x][y]
							destX := offsetX + x
							destY := offsetY + y
							dest := destX*4 + destY*canvasWidth*4
							copy(pixels[dest:], c[:])
						}
					}

					inputs := frameInputs[frameIndex]
					text := strconv.Itoa(frameIndex) + " "
					add := func(b Button, pressed string) {
						if inputs[b] {
							text += pressed
						} else {
							text += strings.ToLower(pressed)
						}
					}
					add(ButtonLeft, "L")
					add(ButtonUp, "U")
					add(ButtonRight, "R")
					add(ButtonDown, "D")
					text += " "
					add(ButtonA, "A")
					add(ButtonB, "B")
					text += " "
					add(ButtonSelect, "E")
					add(ButtonStart, "S")

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

		canvas.SetPixels(pixels)

		win.Update()
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
	rom := getROM()

	// If the CPU profile flag is set, then setup the profiling
	if *cpuprofile != "" {
		startCPUProfiling()
		defer pprof.StopCPUProfile()
	}

	var opts []GameboyOption
	if !*dmgMode {
		opts = append(opts, WithCGBEnabled())
	}
	if !*mute {
		opts = append(opts, WithSound())
	}

	// Initialise the GameBoy with the flag options
	gameboy, err := NewGameboy(rom, opts...)
	if err != nil {
		log.Fatal(err)
	}
	if *stepThrough {
		gameboy.Debug.OutputOpcodes = true
	}

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
