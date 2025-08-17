package main

func BitIsSet(value, bit byte) bool {
	return value&(1<<bit) != 0
}

func BitValue(value, bit byte) byte {
	return (value >> bit) & 1
}

func SetBit(value, bit byte) byte {
	return value | (1 << bit)
}

func ResetBit(value, bit byte) byte {
	return value & ^(1 << bit)
}

func BoolToBit(b bool) byte {
	if b {
		return 1
	}
	return 0
}
