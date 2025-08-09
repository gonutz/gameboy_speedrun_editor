package main

import (
	"flag"
	"log"
	"os"
	"runtime/pprof"
	"time"

	"github.com/faiface/mainthread"
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
	pixelgl.Run(start)
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

	monitor.Gameboy = gameboy
	startGBLoop(gameboy, monitor)
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
