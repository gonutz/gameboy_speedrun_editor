package main

import (
	"log"
	"os"
	"time"
)

// Mode represents the types of mode the GameBoy can run in.
type Mode byte

const (
	// DMG is the mode for the original GameBoy.
	DMG Mode = 1 << iota
	// CGB is the mode for the GameBoy Color.
	CGB
)

type MemoryBankType byte

const (
	romOnly MemoryBankType = iota
	mbc1
	mbc2
	mbc3
	mbc5
)

// globalROM is the cartridge data. It is read-only and never changes throughout
// the run of the Gameboy game. Thus we do not make it part of the Gameboy
// state. Instead we use this global variable throughout the program.
var globalROM []byte

// Cart represents a GameBoy cartridge.
//
// The cartridge is an extension of a banking controller which determines how the cart
// reacts with memory banking. The banking controller provides methods for reading and
// writing data to the cartridge, along with extra functionality such as RTC (real
// time clock).
type Cart struct {
	Mode       Mode
	MemoryBank MemoryBankType
	ROMBank    uint32
	ROMBanking bool
	RAM        [0x20000]byte
	RAMBank    uint32
	RAMEnabled bool
	RTC        [0x10]byte
	LatchedRtc [0x10]byte
	Latched    bool
}

// Read returns a value at a memory address in the ROM.
func (c *Cart) Read(address uint16) byte {
	switch c.MemoryBank {
	case romOnly:
		return globalROM[address]
	case mbc1:
		switch {
		case address < 0x4000:
			return globalROM[address] // Bank 0 is fixed
		case address < 0x8000:
			return globalROM[uint32(address-0x4000)+(c.ROMBank*0x4000)] // Use selected rom bank
		default:
			return c.RAM[(0x2000*c.RAMBank)+uint32(address-0xA000)] // Use selected ram bank
		}
	case mbc2:
		switch {
		case address < 0x4000:
			return globalROM[address] // Bank 0 is fixed
		case address < 0x8000:
			return globalROM[uint32(address-0x4000)+(c.ROMBank*0x4000)] // Use selected rom bank
		default:
			return c.RAM[address-0xA000] // Use ram
		}
	case mbc3:
		switch {
		case address < 0x4000:
			return globalROM[address] // Bank 0 is fixed
		case address < 0x8000:
			return globalROM[uint32(address-0x4000)+(c.ROMBank*0x4000)] // Use selected rom bank
		default:
			if c.RAMBank >= 0x4 {
				if c.Latched {
					return c.LatchedRtc[c.RAMBank]
				}
				return c.RTC[c.RAMBank]
			}
			return c.RAM[(0x2000*c.RAMBank)+uint32(address-0xA000)] // Use selected ram bank
		}
	case mbc5:
		switch {
		case address < 0x4000:
			return globalROM[address] // Bank 0 is fixed
		case address < 0x8000:
			return globalROM[uint32(address-0x4000)+(c.ROMBank*0x4000)] // Use selected rom bank
		default:
			return c.RAM[(0x2000*c.RAMBank)+uint32(address-0xA000)] // Use selected ram bank
		}
	default:
		panic("unknown memory bank type")
	}
}

func (c *Cart) updateRomBankIfZero() {
	if c.ROMBank == 0x00 || c.ROMBank == 0x20 || c.ROMBank == 0x40 || c.ROMBank == 0x60 {
		c.ROMBank++
	}
}

func (c *Cart) WriteROM(address uint16, value byte) {
	switch c.MemoryBank {
	case romOnly:
	case mbc1:
		switch {
		case address < 0x2000:
			// RAM enable
			if value&0xF == 0xA {
				c.RAMEnabled = true
			} else if value&0xF == 0x0 {
				c.RAMEnabled = false
			}
		case address < 0x4000:
			// ROM bank number (lower 5)
			c.ROMBank = (c.ROMBank & 0xe0) | uint32(value&0x1f)
			c.updateRomBankIfZero()
		case address < 0x6000:
			// ROM/RAM banking
			if c.ROMBanking {
				c.ROMBank = (c.ROMBank & 0x1F) | uint32(value&0xe0)
				c.updateRomBankIfZero()
			} else {
				c.RAMBank = uint32(value & 0x3)
			}
		case address < 0x8000:
			// ROM/RAM select mode
			c.ROMBanking = value&0x1 == 0x00
			if c.ROMBanking {
				c.RAMBank = 0
			} else {
				c.ROMBank = c.ROMBank & 0x1F
			}
		}
	case mbc2:
		switch {
		case address < 0x2000:
			// RAM enable
			if address&0x100 == 0 {
				if value&0xF == 0xA {
					c.RAMEnabled = true
				} else if value&0xF == 0x0 {
					c.RAMEnabled = false
				}
			}
			return
		case address < 0x4000:
			// ROM bank number (lower 4)
			if address&0x100 == 0x100 {
				c.ROMBank = uint32(value & 0xF)
				if c.ROMBank == 0x00 || c.ROMBank == 0x20 || c.ROMBank == 0x40 || c.ROMBank == 0x60 {
					c.ROMBank++
				}
			}
			return
		}
	case mbc3:
		switch {
		case address < 0x2000:
			// RAM enable
			c.RAMEnabled = (value & 0xA) != 0
		case address < 0x4000:
			// ROM bank number (lower 5)
			c.ROMBank = uint32(value & 0x7F)
			if c.ROMBank == 0x00 {
				c.ROMBank++
			}
		case address < 0x6000:
			c.RAMBank = uint32(value)
		case address < 0x8000:
			if value == 0x1 {
				c.Latched = false
			} else if value == 0x0 {
				c.Latched = true
				copy(c.RTC[:], c.LatchedRtc[:])
			}
		}
	case mbc5:
		switch {
		case address < 0x2000:
			// RAM enable
			if value&0xF == 0xA {
				c.RAMEnabled = true
			} else if value&0xF == 0x0 {
				c.RAMEnabled = false
			}
		case address < 0x3000:
			// ROM bank number
			c.ROMBank = (c.ROMBank & 0x100) | uint32(value)
		case address < 0x4000:
			// ROM/RAM banking
			c.ROMBank = (c.ROMBank & 0xFF) | uint32(value&0x01)<<8
		case address < 0x6000:
			c.RAMBank = uint32(value & 0xF)
		}
	default:
		panic("unknown memory bank type")
	}
}

func (c *Cart) WriteRAM(address uint16, value byte) {
	switch c.MemoryBank {
	case romOnly:
	case mbc1:
		if c.RAMEnabled {
			c.RAM[(0x2000*c.RAMBank)+uint32(address-0xA000)] = value
		}
	case mbc2:
		if c.RAMEnabled {
			c.RAM[address-0xA000] = value & 0xF
		}
	case mbc3:
		if c.RAMEnabled {
			if c.RAMBank >= 0x4 {
				c.RTC[c.RAMBank] = value
			} else {
				c.RAM[(0x2000*c.RAMBank)+uint32(address-0xA000)] = value
			}
		}
	case mbc5:
		if c.RAMEnabled {
			c.RAM[(0x2000*c.RAMBank)+uint32(address-0xA000)] = value
		}
	default:
		panic("unknown memory bank type")
	}
}

func (c *Cart) GetSaveData() []byte {
	switch c.MemoryBank {
	case romOnly:
		return []byte{}
	default:
		data := make([]byte, len(c.RAM))
		copy(data, c.RAM[:])
		return data
	}
}

func (c *Cart) LoadSaveData(data []byte) {
	switch c.MemoryBank {
	case romOnly:
	default:
		copy(c.RAM[:], data)
	}
}

// GetSaveFilename returns the name of the file that the game should be saved to. This is
// used for saving and loading save data to the cartridge.
// TODO: do something better here
func (c *Cart) GetSaveFilename() string {
	return "" // TODO Remove this.
}

// GetMode returns the modes that this cart can run in.
func (c *Cart) GetMode() Mode {
	return c.Mode
}

// Attempt to load a save game from the expected location.
func (c *Cart) initGameSaves() {
	saveData, err := os.ReadFile(c.GetSaveFilename())
	if err == nil {
		c.LoadSaveData(saveData)
	}
	// Write the RAM to file every second
	// TODO: improve this behaviour
	ticker := time.NewTicker(time.Second)
	go func() {
		for range ticker.C {
			c.Save()
		}
	}()
}

// Save dumps the carts RAM to the save location.
func (c *Cart) Save() {
	data := c.GetSaveData()
	if len(data) > 0 {
		err := os.WriteFile(c.GetSaveFilename(), data, 0644)
		if err != nil {
			log.Printf("Error saving cartridge RAM: %v", err)
		}
	}
}

// NewCartFromFile loads a cartridge ROM from a file.
func NewCartFromFile(filename string) (Cart, error) {
	rom, err := os.ReadFile(filename)
	if err != nil {
		return Cart{}, err
	}
	return NewCart(rom, filename), nil
}

// NewCart loads a cartridge ROM from a byte array and returns a new cartridge with
// the correct memory banking controller. If the game supports saves, then the
// save file for the cartridge will also be loaded, and the saving loop will be
// started to write the save data back to file.
//
// The function will use the following list to determine which MBC to use. Not
// all of the controllers are supported, and the function will only start the
// save loop for controllers which support RAM+BATTERY.
//
//	0x00  ROM ONLY
//	0x01  MBC1
//	0x02  MBC1+RAM
//	0x03  MBC1+RAM+BATTERY
//	0x05  MBC2
//	0x06  MBC2+BATTERY
//	0x08  ROM+RAM
//	0x09  ROM+RAM+BATTERY
//	0x0B  MMM01
//	0x0C  MMM01+RAM
//	0x0D  MMM01+RAM+BATTERY
//	0x0F  MBC3+TIMER+BATTERY
//	0x10  MBC3+TIMER+RAM+BATTERY
//	0x11  MBC3
//	0x12  MBC3+RAM
//	0x13  MBC3+RAM+BATTERY
//	0x15  MBC4
//	0x16  MBC4+RAM
//	0x17  MBC4+RAM+BATTERY
//	0x19  MBC5
//	0x1A  MBC5+RAM
//	0x1B  MBC5+RAM+BATTERY
//	0x1C  MBC5+RUMBLE
//	0x1D  MBC5+RUMBLE+RAM
//	0x1E  MBC5+RUMBLE+RAM+BATTERY
//	0xFC  POCKET CAMERA
//	0xFD  BANDAI TAMA5
//	0xFE  HuC3
//	0xFF  HuC1+RAM+BATTERY
func NewCart(rom []byte, filename string) Cart {
	cartridge := Cart{}

	// Check for GB mode
	switch rom[0x0143] {
	case 0x80:
		cartridge.Mode = DMG | CGB
	case 0xC0:
		cartridge.Mode = CGB
	default:
		cartridge.Mode = DMG
	}

	globalROM = rom
	cartridge.ROMBank = 1

	// Determine cartridge type
	mbcFlag := rom[0x147]
	switch mbcFlag {
	case 0x00, 0x08, 0x09, 0x0B, 0x0C, 0x0D:
		cartridge.MemoryBank = romOnly
	default:
		switch {
		case mbcFlag <= 0x03:
			cartridge.MemoryBank = mbc1
		case mbcFlag <= 0x06:
			cartridge.MemoryBank = mbc2
		case mbcFlag <= 0x13:
			cartridge.MemoryBank = mbc3
		case mbcFlag < 0x17:
			log.Println("Warning: MBC4 carts are not supported.")
			cartridge.MemoryBank = mbc1
		case mbcFlag < 0x1F:
			cartridge.MemoryBank = mbc5
		default:
			log.Printf("Warning: This cart may not be supported: %02x", mbcFlag)
			cartridge.MemoryBank = mbc1
		}
	}

	switch mbcFlag {
	case 0x3, 0x6, 0x9, 0xD, 0xF, 0x10, 0x13, 0x17, 0x1B, 0x1E, 0xFF:
		cartridge.initGameSaves()
	}
	return cartridge
}
