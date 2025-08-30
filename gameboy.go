package main

import "fmt"

const (
	// ClockSpeed is the number of cycles the GameBoy CPU performs each second.
	ClockSpeed = 4194304
	// FramesSecond is the target number of frames for each frame of GameBoy output.
	FramesSecond = 60
	// CyclesPerFrame is the number of CPU cycles in each frame.
	CyclesPerFrame = ClockSpeed / FramesSecond
)

// NewGameboy returns a new Gameboy instance.
func NewGameboy(rom []byte, opts GameboyOptions) Gameboy {
	gameboy := Gameboy{Options: opts}
	gameboy.init(rom)
	return gameboy
}

type GameboyOptions struct {
	Sound   bool
	CGBMode bool
}

// Gameboy is the master struct which contains all of the sub components
// for running the Gameboy emulator.
type Gameboy struct {
	Options GameboyOptions

	Memory Memory
	CPU    CPU
	Sound  APU

	TimerCounter int32

	// Matrix of pixel data which is used while the screen is rendering. When a
	// frame has been completed, this data is copied into the PreparedData matrix.
	ScreenData [ScreenWidth][ScreenHeight][3]uint8
	BGPriority [ScreenWidth][ScreenHeight]bool

	// Track colour of tiles in scanline for priority management.
	TileScanline    [ScreenWidth]uint8
	ScanlineCounter int32
	ScreenCleared   bool

	// PreparedData is a matrix of screen pixel data for a single frame which has
	// been fully rendered.
	PreparedData [ScreenWidth][ScreenHeight][3]uint8

	InterruptsEnabling bool
	InterruptsOn       bool
	Halted             bool

	// Mask of the currenly pressed buttons.
	InputMask byte

	// Flag if the game is running in cgb mode. For this to be true the game
	// rom must support cgb mode and the option be true.
	CGBMode       bool
	BGPalette     CGBPalette
	SpritePalette CGBPalette

	CurrentSpeed byte
	PrepareSpeed bool

	ThisCpuTicks int32

	ExtraCycles int32
}

// Update update the state of the gameboy by a single frame.
func (gb *Gameboy) Update() int {
	cycles := int(gb.ExtraCycles)
	for cycles < CyclesPerFrame {
		cyclesOp := 4
		if !gb.Halted {
			cyclesOp = gb.ExecuteNextOpcode()
		} else {
			// TODO: This is incorrect
		}
		cycles += cyclesOp
		gb.updateGraphics(cyclesOp)
		gb.updateTimers(cyclesOp)
		cycles += gb.doInterrupts()
	}
	gb.ExtraCycles = int32(cycles - CyclesPerFrame)
	return cycles
}

// BGMapString returns a string of the values in the background map.
func (gb *Gameboy) BGMapString() string {
	out := ""
	for y := uint16(0); y < 0x20; y++ {
		out += fmt.Sprintf("%2x: ", y)
		for x := uint16(0); x < 0x20; x++ {
			out += fmt.Sprintf("%2x ", gb.Memory.Read(gb, 0x9800+(y*0x20)+x))
		}
		out += "\n"
	}
	return out
}

// Get the current CPU speed multiplier (either 1 or 2).
func (gb *Gameboy) getSpeed() int {
	return int(gb.CurrentSpeed + 1)
}

// Check if the speed needs to be switched for CGB mode.
func (gb *Gameboy) checkSpeedSwitch() {
	if gb.PrepareSpeed {
		// Switch speed
		gb.PrepareSpeed = false
		if gb.CurrentSpeed == 0 {
			gb.CurrentSpeed = 1
		} else {
			gb.CurrentSpeed = 0
		}
		gb.Halted = false
	}
}

func (gb *Gameboy) updateTimers(cycles int) {
	gb.dividerRegister(cycles)
	if gb.isClockEnabled() {
		gb.TimerCounter += int32(cycles)

		freq := gb.getClockFreqCount()
		for gb.TimerCounter >= int32(freq) {
			gb.TimerCounter -= int32(freq)
			tima := gb.Memory.Read(gb, TIMA)
			if tima == 0xFF {
				gb.Memory.HighRAM[TIMA-0xFF00] = gb.Memory.Read(gb, TMA)
				gb.requestInterrupt(2)
			} else {
				gb.Memory.HighRAM[TIMA-0xFF00] = tima + 1
			}
		}
	}
}

func (gb *Gameboy) isClockEnabled() bool {
	return BitIsSet(gb.Memory.Read(gb, TAC), 2)
}

func (gb *Gameboy) getClockFreq() byte {
	return gb.Memory.Read(gb, TAC) & 0x3
}

func (gb *Gameboy) getClockFreqCount() int {
	switch gb.getClockFreq() {
	case 0:
		return 1024
	case 1:
		return 16
	case 2:
		return 64
	default:
		return 256
	}
}

func (gb *Gameboy) setClockFreq() {
	gb.TimerCounter = 0
}

func (gb *Gameboy) dividerRegister(cycles int) {
	gb.CPU.Divider += int32(cycles)
	if gb.CPU.Divider >= 255 {
		gb.CPU.Divider -= 255
		gb.Memory.HighRAM[DIV-0xFF00]++
	}
}

// Request the Gameboy to perform an interrupt.
func (gb *Gameboy) requestInterrupt(interrupt byte) {
	req := gb.Memory.ReadHighRam(gb, 0xFF0F)
	req = SetBit(req, interrupt)
	gb.Memory.Write(gb, 0xFF0F, req)
}

func (gb *Gameboy) doInterrupts() (cycles int) {
	if gb.InterruptsEnabling {
		gb.InterruptsOn = true
		gb.InterruptsEnabling = false
		return 0
	}
	if !gb.InterruptsOn && !gb.Halted {
		return 0
	}

	req := gb.Memory.ReadHighRam(gb, 0xFF0F)
	enabled := gb.Memory.ReadHighRam(gb, 0xFFFF)

	if req > 0 {
		for i := byte(0); i < 5; i++ {
			if BitIsSet(req, i) && BitIsSet(enabled, i) {
				gb.serviceInterrupt(i)
				return 20
			}
		}
	}
	return 0
}

// Address that should be jumped to by interrupt.
var interruptAddresses = map[byte]uint16{
	0: 0x40, // V-Blank
	1: 0x48, // LCDC Status
	2: 0x50, // Timer Overflow
	3: 0x58, // Serial Transfer
	4: 0x60, // Hi-Lo P10-P13
}

// Called if an interrupt has been raised. Will check if interrupts are
// enabled and will jump to the interrupt address.
func (gb *Gameboy) serviceInterrupt(interrupt byte) {
	// If was halted without interrupts, do not jump or reset IF
	if !gb.InterruptsOn && gb.Halted {
		gb.Halted = false
		return
	}
	gb.InterruptsOn = false
	gb.Halted = false

	req := gb.Memory.ReadHighRam(gb, 0xFF0F)
	req = ResetBit(req, interrupt)
	gb.Memory.Write(gb, 0xFF0F, req)

	gb.pushStack(gb.CPU.PC)
	gb.CPU.PC = interruptAddresses[interrupt]
}

// Push a 16 bit value onto the stack and decrement SP.
func (gb *Gameboy) pushStack(address uint16) {
	sp := gb.CPU.SP.HiLo()
	gb.Memory.Write(gb, sp-1, byte(uint16(address&0xFF00)>>8))
	gb.Memory.Write(gb, sp-2, byte(address&0xFF))
	gb.CPU.SP.Set(gb.CPU.SP.HiLo() - 2)
}

// Pop the next 16 bit value off the stack and increment SP.
func (gb *Gameboy) popStack() uint16 {
	sp := gb.CPU.SP.HiLo()
	byte1 := uint16(gb.Memory.Read(gb, sp))
	byte2 := uint16(gb.Memory.Read(gb, sp+1)) << 8
	gb.CPU.SP.Set(gb.CPU.SP.HiLo() + 2)
	return byte1 | byte2
}

func (gb *Gameboy) joypadValue(current byte) byte {
	var in byte = 0xF
	if BitIsSet(current, 4) {
		in = gb.InputMask & 0xF
	} else if BitIsSet(current, 5) {
		in = (gb.InputMask >> 4) & 0xF
	}
	return current | 0xc0 | in
}

// IsCGB returns if we are using CGB features.
func (gb *Gameboy) IsCGB() bool {
	return gb.CGBMode
}

// Initialise the Gameboy using a path to a rom.
func (gb *Gameboy) init(rom []byte) {
	gb.setup()
	hasCGB := gb.Memory.LoadCart(rom)
	gb.CGBMode = gb.Options.CGBMode && hasCGB
}

// Setup and instantitate the gameboys components.
func (gb *Gameboy) setup() {
	// Initialise the CPU
	gb.CPU = CPU{}
	gb.CPU.Init(gb.Options.CGBMode)

	// Initialise the memory
	gb.Memory = Memory{}
	gb.Memory.Init(gb)

	gb.Sound = APU{}
	gb.Sound.Init(gb.Options.Sound)

	gb.ScanlineCounter = 456
	gb.InputMask = 0xFF

	gb.SpritePalette = NewPalette()
	gb.BGPalette = NewPalette()

	for x := range gb.PreparedData {
		for y := range gb.PreparedData[x] {
			gb.PreparedData[x][y] = ColorPalette[3]
			gb.ScreenData[x][y] = ColorPalette[3]
		}
	}
}

// PressButton notifies the GameBoy that a button has just been pressed
// and requests a joypad interrupt.
func (gb *Gameboy) PressButton(button Button) {
	gb.InputMask = ResetBit(gb.InputMask, byte(button))
	gb.requestInterrupt(4)
}

// ReleaseButton notifies the GameBoy that a button has just been released.
func (gb *Gameboy) ReleaseButton(button Button) {
	gb.InputMask = SetBit(gb.InputMask, byte(button))
}

// Button represents the button on a GameBoy.
type Button byte

const (
	ButtonA Button = iota
	ButtonB
	ButtonSelect
	ButtonStart
	ButtonRight
	ButtonLeft
	ButtonUp
	ButtonDown

	buttonCount // NOTE This has to come last.
)
