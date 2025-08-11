package main

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

// PressButton notifies the GameBoy that a button has just been pressed
// and requests a joypad interrupt.
func (gb *Gameboy) PressButton(button Button) {
	gb.inputMask = ResetBit(gb.inputMask, byte(button))
	gb.requestInterrupt(4) // Request the joypad interrupt
}

// ReleaseButton notifies the GameBoy that a button has just been released.
func (gb *Gameboy) ReleaseButton(button Button) {
	gb.inputMask = SetBit(gb.inputMask, byte(button))
}
