package main

var ColorPalette = [4][3]byte{
	{0xE0, 0xF8, 0xD0},
	{0x88, 0xC0, 0x70},
	{0x34, 0x68, 0x56},
	{0x08, 0x18, 0x20},
}

// NewPalette makes a new CGB colour palette.
func NewPalette() CGBPalette {
	p := CGBPalette{}
	for i := range p.Palette {
		p.Palette[i] = 0xFF
	}
	return p
}

// Palette for cgb containing information tracking the palette colour info.
type CGBPalette struct {
	// Palette colour information.
	Palette [0x40]byte
	// Current index the palette is referencing.
	Index byte
	// If to auto increment on write.
	Inc bool
}

// Update the index the palette is indexing and set
// auto increment if bit 7 is set.
func (pal *CGBPalette) updateIndex(value byte) {
	pal.Index = value & 0x3F
	pal.Inc = BitIsSet(value, 7)
}

// Read the palette information stored at the current index.
func (pal *CGBPalette) read() byte {
	return pal.Palette[pal.Index]
}

// Read the current index.
func (pal *CGBPalette) readIndex() byte {
	return pal.Index
}

// Write a value to the palette at the current index.
func (pal *CGBPalette) write(value byte) {
	pal.Palette[pal.Index] = value
	if pal.Inc {
		pal.Index = (pal.Index + 1) & 0x3F
	}
}

// Get the rgb colour for a palette at a colour number.
func (pal *CGBPalette) get(palette byte, num byte) (uint8, uint8, uint8) {
	idx := (palette * 8) + (num * 2)
	colour := uint16(pal.Palette[idx]) | uint16(pal.Palette[idx+1])<<8
	r := uint8(colour & 0x1F)
	g := uint8((colour >> 5) & 0x1F)
	b := uint8((colour >> 10) & 0x1F)
	return colArr[r], colArr[g], colArr[b]
}

// Mapping of the 5 bit colour value to a 8 bit value.
var colArr = []uint8{
	0x00,
	0x08,
	0x10,
	0x18,
	0x20,
	0x29,
	0x31,
	0x39,
	0x41,
	0x4A,
	0x52,
	0x5A,
	0x62,
	0x6A,
	0x73,
	0x7B,
	0x83,
	0x8B,
	0x94,
	0x9C,
	0xA4,
	0xAC,
	0xB4,
	0xBD,
	0xC5,
	0xCD,
	0xD5,
	0xDE,
	0xE6,
	0xEE,
	0xF6,
	0xFF,
}
