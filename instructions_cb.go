package main

func instRlc(gb *Gameboy, setter func(gb *Gameboy, value byte), val byte) {
	carry := val >> 7
	rot := (val<<1)&0xFF | carry
	setter(gb, rot)

	gb.CPU.SetZ(rot == 0)
	gb.CPU.SetN(false)
	gb.CPU.SetH(false)
	gb.CPU.SetC(carry == 1)
}

func instRl(gb *Gameboy, setter func(gb *Gameboy, value byte), val byte) {
	newCarry := val >> 7
	oldCarry := B(gb.CPU.C())
	rot := (val<<1)&0xFF | oldCarry
	setter(gb, rot)

	gb.CPU.SetZ(rot == 0)
	gb.CPU.SetN(false)
	gb.CPU.SetH(false)
	gb.CPU.SetC(newCarry == 1)
}

func instRrc(gb *Gameboy, setter func(gb *Gameboy, value byte), val byte) {
	carry := val & 1
	rot := (val >> 1) | (carry << 7)
	setter(gb, rot)

	gb.CPU.SetZ(rot == 0)
	gb.CPU.SetN(false)
	gb.CPU.SetH(false)
	gb.CPU.SetC(carry == 1)
}

func instRr(gb *Gameboy, setter func(gb *Gameboy, value byte), val byte) {
	newCarry := val & 1
	oldCarry := B(gb.CPU.C())
	rot := (val >> 1) | (oldCarry << 7)
	setter(gb, rot)

	gb.CPU.SetZ(rot == 0)
	gb.CPU.SetN(false)
	gb.CPU.SetH(false)
	gb.CPU.SetC(newCarry == 1)
}

func instSla(gb *Gameboy, setter func(gb *Gameboy, value byte), val byte) {
	carry := val >> 7
	rot := (val << 1) & 0xFF
	setter(gb, rot)

	gb.CPU.SetZ(rot == 0)
	gb.CPU.SetN(false)
	gb.CPU.SetH(false)
	gb.CPU.SetC(carry == 1)
}

func instSra(gb *Gameboy, setter func(gb *Gameboy, value byte), val byte) {
	rot := (val & 128) | (val >> 1)
	setter(gb, rot)

	gb.CPU.SetZ(rot == 0)
	gb.CPU.SetN(false)
	gb.CPU.SetH(false)
	gb.CPU.SetC(val&1 == 1)
}

func instSrl(gb *Gameboy, setter func(gb *Gameboy, value byte), val byte) {
	carry := val & 1
	rot := val >> 1
	setter(gb, rot)

	gb.CPU.SetZ(rot == 0)
	gb.CPU.SetN(false)
	gb.CPU.SetH(false)
	gb.CPU.SetC(carry == 1)
}

func (gb *Gameboy) instBit(bit byte, val byte) {
	gb.CPU.SetZ((val>>bit)&1 == 0)
	gb.CPU.SetN(false)
	gb.CPU.SetH(true)
}

func instSwap(gb *Gameboy, setter func(gb *Gameboy, value byte), val byte) {
	swapped := val<<4&240 | val>>4
	setter(gb, swapped)

	gb.CPU.SetZ(swapped == 0)
	gb.CPU.SetN(false)
	gb.CPU.SetH(false)
	gb.CPU.SetC(false)
}

func cbInstructions() [0x100]func(gb *Gameboy) {
	instructions := [0x100]func(gb *Gameboy){}

	getMap := [8]func(gb *Gameboy) byte{
		func(gb *Gameboy) byte { return gb.CPU.BC.Hi() },
		func(gb *Gameboy) byte { return gb.CPU.BC.Lo() },
		func(gb *Gameboy) byte { return gb.CPU.DE.Hi() },
		func(gb *Gameboy) byte { return gb.CPU.DE.Lo() },
		func(gb *Gameboy) byte { return gb.CPU.HL.Hi() },
		func(gb *Gameboy) byte { return gb.CPU.HL.Lo() },
		func(gb *Gameboy) byte { return gb.Memory.Read(gb, gb.CPU.HL.HiLo()) },
		func(gb *Gameboy) byte { return gb.CPU.AF.Hi() },
	}
	setMap := [8]func(gb *Gameboy, value byte){
		func(gb *Gameboy, value byte) { gb.CPU.BC.SetHi(value) },
		func(gb *Gameboy, value byte) { gb.CPU.BC.SetLo(value) },
		func(gb *Gameboy, value byte) { gb.CPU.DE.SetHi(value) },
		func(gb *Gameboy, value byte) { gb.CPU.DE.SetLo(value) },
		func(gb *Gameboy, value byte) { gb.CPU.HL.SetHi(value) },
		func(gb *Gameboy, value byte) { gb.CPU.HL.SetLo(value) },
		func(gb *Gameboy, value byte) { gb.Memory.Write(gb, gb.CPU.HL.HiLo(), value) },
		func(gb *Gameboy, value byte) { gb.CPU.AF.SetHi(value) },
	}

	for x := 0; x < 8; x++ {
		// Store x so it can be used in the function scopes
		var i = x

		instructions[0x00+i] = func(gb *Gameboy) { instRlc(gb, setMap[i], getMap[i](gb)) }
		instructions[0x08+i] = func(gb *Gameboy) { instRrc(gb, setMap[i], getMap[i](gb)) }
		instructions[0x10+i] = func(gb *Gameboy) { instRl(gb, setMap[i], getMap[i](gb)) }
		instructions[0x18+i] = func(gb *Gameboy) { instRr(gb, setMap[i], getMap[i](gb)) }
		instructions[0x20+i] = func(gb *Gameboy) { instSla(gb, setMap[i], getMap[i](gb)) }
		instructions[0x28+i] = func(gb *Gameboy) { instSra(gb, setMap[i], getMap[i](gb)) }
		instructions[0x30+i] = func(gb *Gameboy) { instSwap(gb, setMap[i], getMap[i](gb)) }
		instructions[0x38+i] = func(gb *Gameboy) { instSrl(gb, setMap[i], getMap[i](gb)) }

		// BIT instructions
		instructions[0x40+i] = func(gb *Gameboy) { gb.instBit(0, getMap[i](gb)) }
		instructions[0x48+i] = func(gb *Gameboy) { gb.instBit(1, getMap[i](gb)) }
		instructions[0x50+i] = func(gb *Gameboy) { gb.instBit(2, getMap[i](gb)) }
		instructions[0x58+i] = func(gb *Gameboy) { gb.instBit(3, getMap[i](gb)) }
		instructions[0x60+i] = func(gb *Gameboy) { gb.instBit(4, getMap[i](gb)) }
		instructions[0x68+i] = func(gb *Gameboy) { gb.instBit(5, getMap[i](gb)) }
		instructions[0x70+i] = func(gb *Gameboy) { gb.instBit(6, getMap[i](gb)) }
		instructions[0x78+i] = func(gb *Gameboy) { gb.instBit(7, getMap[i](gb)) }

		// RES instructions
		instructions[0x80+i] = func(gb *Gameboy) { setMap[i](gb, Reset(getMap[i](gb), 0)) }
		instructions[0x88+i] = func(gb *Gameboy) { setMap[i](gb, Reset(getMap[i](gb), 1)) }
		instructions[0x90+i] = func(gb *Gameboy) { setMap[i](gb, Reset(getMap[i](gb), 2)) }
		instructions[0x98+i] = func(gb *Gameboy) { setMap[i](gb, Reset(getMap[i](gb), 3)) }
		instructions[0xA0+i] = func(gb *Gameboy) { setMap[i](gb, Reset(getMap[i](gb), 4)) }
		instructions[0xA8+i] = func(gb *Gameboy) { setMap[i](gb, Reset(getMap[i](gb), 5)) }
		instructions[0xB0+i] = func(gb *Gameboy) { setMap[i](gb, Reset(getMap[i](gb), 6)) }
		instructions[0xB8+i] = func(gb *Gameboy) { setMap[i](gb, Reset(getMap[i](gb), 7)) }

		// SET instructions
		instructions[0xC0+i] = func(gb *Gameboy) { setMap[i](gb, Set(getMap[i](gb), 0)) }
		instructions[0xC8+i] = func(gb *Gameboy) { setMap[i](gb, Set(getMap[i](gb), 1)) }
		instructions[0xD0+i] = func(gb *Gameboy) { setMap[i](gb, Set(getMap[i](gb), 2)) }
		instructions[0xD8+i] = func(gb *Gameboy) { setMap[i](gb, Set(getMap[i](gb), 3)) }
		instructions[0xE0+i] = func(gb *Gameboy) { setMap[i](gb, Set(getMap[i](gb), 4)) }
		instructions[0xE8+i] = func(gb *Gameboy) { setMap[i](gb, Set(getMap[i](gb), 5)) }
		instructions[0xF0+i] = func(gb *Gameboy) { setMap[i](gb, Set(getMap[i](gb), 6)) }
		instructions[0xF8+i] = func(gb *Gameboy) { setMap[i](gb, Set(getMap[i](gb), 7)) }
	}

	return instructions
}
