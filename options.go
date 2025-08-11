package main

// GameboyOption is an option for the Gameboy execution.
type GameboyOption func(o *gameboyOptions)

type gameboyOptions struct {
	sound   bool
	cgbMode bool
}

// DebugFlags are flags which can be set to alter the execution of the Gameboy.
type DebugFlags struct {
	// HideSprites turns off rendering of sprites to the display.
	HideSprites bool

	// HideBackground turns off rendering of background tiles to the display.
	HideBackground bool

	// OutputOpcodes will log the current opcode to the console on each tick.
	// This will slow down execution massively so is only used for debugging
	// issues with the emulation.
	OutputOpcodes bool
}

// WithCGBEnabled runs the Gameboy with cgb mode enabled.
func WithCGBEnabled() GameboyOption {
	return func(o *gameboyOptions) {
		o.cgbMode = true
	}
}

// WithSound runs the Gameboy with sound output.
func WithSound() GameboyOption {
	return func(o *gameboyOptions) {
		o.sound = true
	}
}
