package main

// Register represents a GB CPU 16bit Register which provides functions
// for setting and getting the higher and lower bytes.
type Register struct {
	// The Value of the register.
	Value uint16

	// A Mask over the possible values in the register.
	// Only used for the AF register where lower bits of
	// F cannot be set.
	Mask uint16
}

// Hi gets the higher byte of the register.
func (reg *Register) Hi() byte {
	return byte(reg.Value >> 8)
}

// Lo gets the lower byte of the register.
func (reg *Register) Lo() byte {
	return byte(reg.Value & 0xFF)
}

// HiLo gets the 2 byte value of the register.
func (reg *Register) HiLo() uint16 {
	return reg.Value
}

// SetHi sets the higher byte of the register.
func (reg *Register) SetHi(val byte) {
	reg.Value = uint16(val)<<8 | (uint16(reg.Value) & 0xFF)
	reg.updateMask()
}

// SetLog sets the lower byte of the register.
func (reg *Register) SetLo(val byte) {
	reg.Value = uint16(val) | (uint16(reg.Value) & 0xFF00)
	reg.updateMask()
}

// Set the value of the register.
func (reg *Register) Set(val uint16) {
	reg.Value = val
	reg.updateMask()
}

// Mask the value if one is set on this register.
func (reg *Register) updateMask() {
	if reg.Mask != 0 {
		reg.Value &= reg.Mask
	}
}

// CPU contains the registers used for program execution and
// provides methods for setting flags.
type CPU struct {
	AF      Register
	BC      Register
	DE      Register
	HL      Register
	PC      uint16
	SP      Register
	Divider int32
}

// Init CPU and its registers to the initial values.
func (cpu *CPU) Init(cgb bool) {
	cpu.PC = 0x100
	if cgb {
		cpu.AF.Set(0x1180)
	} else {
		cpu.AF.Set(0x01B0)
	}
	cpu.BC.Set(0x0000)
	cpu.DE.Set(0xFF56)
	cpu.HL.Set(0x000D)
	cpu.SP.Set(0xFFFE)

	cpu.AF.Mask = 0xFFF0
}

// Internally set the value of a flag on the flag register.
func (cpu *CPU) setFlag(index byte, on bool) {
	if on {
		cpu.AF.SetLo(SetBit(cpu.AF.Lo(), index))
	} else {
		cpu.AF.SetLo(ResetBit(cpu.AF.Lo(), index))
	}
}

// SetZ sets the value of the Z flag.
func (cpu *CPU) SetZ(on bool) {
	cpu.setFlag(7, on)
}

// SetN sets the value of the N flag.
func (cpu *CPU) SetN(on bool) {
	cpu.setFlag(6, on)
}

// SetH sets the value of the H flag.
func (cpu *CPU) SetH(on bool) {
	cpu.setFlag(5, on)
}

// SetC sets the value of the C flag.
func (cpu *CPU) SetC(on bool) {
	cpu.setFlag(4, on)
}

// Z gets the value of the Z flag.
func (cpu *CPU) Z() bool {
	return cpu.AF.HiLo()>>7&1 == 1
}

// N gets the value of the N flag.
func (cpu *CPU) N() bool {
	return cpu.AF.HiLo()>>6&1 == 1
}

// H gets the value of the H flag.
func (cpu *CPU) H() bool {
	return cpu.AF.HiLo()>>5&1 == 1
}

// C gets the value of the C flag.
func (cpu *CPU) C() bool {
	return cpu.AF.HiLo()>>4&1 == 1
}
