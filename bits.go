package main

// BitIsSet if a bit is set.
func BitIsSet(value byte, bit byte) bool {
	return (value>>bit)&1 == 1
}

// BitValue returns the value of a bit bit.
func BitValue(value byte, bit byte) byte {
	return (value >> bit) & 1
}

// SetBit a bit and return the new value.
func SetBit(value byte, bit byte) byte {
	return value | (1 << bit)
}

// ResetBit a bit and return the new value.
func ResetBit(value byte, bit byte) byte {
	return value & ^(1 << bit)
}

// HalfCarryAdd half carries two values.
func HalfCarryAdd(val1 byte, val2 byte) bool {
	return (val1&0xF)+(val2&0xF) > 0xF
}

// BoolToBit transforms a bool into a 1/0 byte.
func BoolToBit(val bool) byte {
	if val {
		return 1
	}
	return 0
}
