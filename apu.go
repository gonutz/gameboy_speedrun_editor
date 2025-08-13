package main

import (
	"log"
	"math"
	"math/rand"
	"time"

	"github.com/hajimehoshi/oto"
)

const (
	sampleRate = 44100
	twoPi      = 2 * math.Pi
	perSample  = 1 / float64(sampleRate)
)

// APU is the GameBoy's audio processing unit. Audio is comprised of four
// channels, each one controlled by a set of registers.
//
// Channels 1 and 2 are both Square channels, channel 3 is a arbitrary
// waveform channel which can be set in RAM, and channel 4 outputs noise.
type APU struct {
	Memory      [52]byte
	Channel1    Channel
	Channel2    Channel
	Channel3    Channel
	Channel4    Channel
	LeftVolume  float64
	RightVolume float64
	WaveformRam [0x20]byte
}

// Init the sound emulation for a Gameboy.
func (a *APU) Init(sound bool) {
	for i := range a.WaveformRam {
		a.WaveformRam[i] = 0
	}

	// Sets waveform ram to:
	// 00 FF 00 FF  00 FF 00 FF  00 FF 00 FF  00 FF 00 FF
	for x := 0x0; x < 0x20; x++ {
		if x&2 == 0 {
			a.WaveformRam[x] = 0x00
		} else {
			a.WaveformRam[x] = 0xFF
		}
	}

	// Create the channels with their sounds
	a.Channel1 = NewChannel()
	a.Channel2 = NewChannel()
	a.Channel3 = NewChannel()
	a.Channel4 = NewChannel()

	if sound {
		player, err := oto.NewPlayer(sampleRate, 1, 1, sampleRate/30)
		if err != nil {
			log.Fatalf("Failed to start audio: %v", err)
		}
		go a.play(player)
	}
}

// Time in seconds which to buffer ahead of the emulation.
const bufferTime = 0.05

func (a *APU) play(player *oto.Player) {
	start := time.Now()
	var totalSamples int64 = 0
	for c := range time.Tick(time.Second / 60) {
		// Calculate the expected samples since the start adding on the buffer
		expectedSamples := int64(math.Ceil((c.Sub(start).Seconds() + bufferTime) * sampleRate))
		newSamples := expectedSamples - totalSamples
		totalSamples = expectedSamples
		if newSamples <= 0 {
			continue
		}

		// Populate the buffer by sampling the channels
		buffer := make([]byte, newSamples)
		vol := (a.LeftVolume + a.RightVolume) / 10
		for i := range buffer {
			// TODO: output stereo channels instead of combining
			val := (a.Channel1.Sample(a) + a.Channel2.Sample(a) + a.Channel3.Sample(a) + a.Channel4.Sample(a)) / 4
			buffer[i] = byte(float64(val) * vol)
		}

		// TODO: handle error
		player.Write(buffer)
	}
}

var soundMask = []byte{
	/* 0xFF10 */ 0xFF, 0xC0, 0xFF, 0x00, 0x40,
	/* 0xFF15 */ 0x00, 0xC0, 0xFF, 0x00, 0x40,
	/* 0xFF1A */ 0x80, 0x00, 0x60, 0x00, 0x40,
	/* 0xFF20 */ 0x00, 0x3F, 0xFF, 0xFF, 0x40,
	/* 0xFF24 */ 0xFF, 0xFF, 0x80,
}

var sound3Volume = map[byte]float64{0: 0, 1: 1, 2: 0.5, 3: 0.25}

// Read returns a value from the APU.
func (a *APU) Read(address uint16) byte {
	if address >= 0xFF30 {
		return a.WaveformRam[address-0xFF30]
	}
	return a.Memory[address-0xFF00] & soundMask[address-0xFF10]
}

// Write a value to the APU registers.
func (a *APU) Write(address uint16, value byte) {
	a.Memory[address-0xFF00] = value

	switch address {
	// Channel 1
	case 0xFF14:
		if address == 0xFF14 && value&0x80 == 0x80 {
			a.start1()
		}
		frequencyValue := uint16(a.Memory[0x14]&0x7)<<8 | uint16(a.Memory[0x13])
		a.Channel1.Frequency = 131072 / (2048 - float64(frequencyValue))
	case 0xFF11:
		pattern := (a.Memory[0x11] & 0xC0) >> 6
		a.Channel1.Generator = Square(squareLimits[pattern])

	// Channel 2
	case 0xFF19:
		if address == 0xFF19 && value&0x80 == 0x80 {
			a.start2()
		}
		frequencyValue := uint16(a.Memory[0x19]&0x7)<<8 | uint16(a.Memory[0x18])
		a.Channel2.Frequency = 131072 / (2048 - float64(frequencyValue))
	case 0xFF16:
		pattern := (a.Memory[0x16] & 0xC0) >> 6
		a.Channel2.Generator = Square(squareLimits[pattern])

	// Channel 3
	case 0xFF1A:
		// TODO: simplify
		soundOn := a.Memory[0x1A]&0x80 == 0x80
		if soundOn {
			a.Channel3.EnvelopeStepsInit = 1
		} else {
			a.Channel3.EnvelopeStepsInit = 0
		}
	case 0xFF1E, 0xFF1F:
		if address == 0xFF1E && value&0x80 == 0x80 {
			a.start3()
		}
		frequencyValue := uint16(a.Memory[0x1E]&0x7)<<8 | uint16(a.Memory[0x1D])
		a.Channel3.Frequency = 65536 / (2048 - float64(frequencyValue))
	case 0xFF1C:
		// Output level
		value := (a.Memory[0x1C] & 0x60) >> 5
		a.Channel3.Amplitude = sound3Volume[value]

	// Channel 4
	case 0xFF22:
		shiftClock := float64((value & 0xF0) >> 4)
		// TODO: counter step width
		divRatio := float64(value & 0x7)
		if divRatio == 0 {
			divRatio = 0.5
		}
		a.Channel4.Frequency = 524288 / divRatio / math.Pow(2, shiftClock+1)
	case 0xFF23:
		if value&0x80 == 0x80 {
			a.Channel4.Generator = Noise()
			a.start4()
		}

	case 0xFF24:
		// Volume control
		a.LeftVolume = float64((a.Memory[0x24]&0x70)>>4) / 7
		a.RightVolume = float64(a.Memory[0x24]&0x7) / 7

	case 0xFF25:
		// Channel control
		// Right output for each channel
		output1r := a.Memory[0x25]&0x1 == 0x1
		output2r := a.Memory[0x25]&0x2 == 0x2
		output3r := a.Memory[0x25]&0x4 == 0x4
		output4r := a.Memory[0x25]&0x8 == 0x8

		// Left output for each channel
		output1l := a.Memory[0x25]&0x10 == 0x10
		output2l := a.Memory[0x25]&0x20 == 0x20
		output3l := a.Memory[0x25]&0x40 == 0x40
		output4l := a.Memory[0x25]&0x80 == 0x80

		a.Channel1.On = output1r || output1l
		a.Channel2.On = output2r || output2l
		a.Channel3.On = output3r || output3l
		a.Channel4.On = output4r || output4l
	}
	// TODO: if writing to FF26 bit 7 destroy all contents (also cannot access)
}

// WriteWaveform writes a value to the waveform ram.
func (a *APU) WriteWaveform(address uint16, value byte) {
	soundIndex := (address - 0xFF30) * 2
	a.WaveformRam[soundIndex] = byte((value>>4)&0xF) * 0x11
	a.WaveformRam[soundIndex+1] = byte(value&0xF) * 0x11
}

// ToggleSoundChannel toggles a sound channel for debugging.
func (a *APU) ToggleSoundChannel(channel int) {
	switch channel {
	case 1:
		a.Channel1.DebugOff = !a.Channel1.DebugOff
	case 2:
		a.Channel2.DebugOff = !a.Channel2.DebugOff
	case 3:
		a.Channel3.DebugOff = !a.Channel3.DebugOff
	case 4:
		a.Channel4.DebugOff = !a.Channel4.DebugOff
	}
	log.Printf("Toggle Channel %v mute", channel)
}

// Start the 1st sound channel.
func (a *APU) start1() {
	selection := (a.Memory[0x14] & 0x40) >> 6 // 1 = stop when length in NR11 expires
	length := a.Memory[0x11] & 0x3F

	// Envelope settings
	envVolume, envDirection, envSweep := a.extractEnvelope(a.Memory[0x12])

	// Sweep
	sweepTime := (a.Memory[0x10] & 0x70) >> 4
	sweepDirection := a.Memory[0x10] >> 3 // 1 = decrease
	sweepNumber := a.Memory[0x10] & 0x7

	duration := -1
	if selection == 1 {
		duration = int(float64(length)*(1/64)) * sampleRate
	}

	a.Channel1.Reset(duration)
	a.Channel1.EnvelopeSteps = int32(envVolume)
	a.Channel1.EnvelopeStepsInit = int32(envVolume)
	a.Channel1.EnvelopeSamples = int32(envSweep) * sampleRate / 64
	a.Channel1.EnvelopeIncreasing = envDirection == 1

	a.Channel1.SweepStepLen = sweepTime
	a.Channel1.SweepSteps = sweepNumber
	a.Channel1.SweepIncrease = sweepDirection == 0
}

// Start the 2nd sound channel.
func (a *APU) start2() {
	selection := (a.Memory[0x19] & 0x40) >> 6 // 1 = stop when length in NR24 expires
	length := a.Memory[0x16] & 0x3F

	// Envelope settings
	envVolume, envDirection, envSweep := a.extractEnvelope(a.Memory[0x17])

	duration := -1
	if selection == 1 {
		duration = int(float64(length)*(1/64)) * sampleRate
	}

	a.Channel2.Reset(duration)
	a.Channel2.EnvelopeSteps = int32(envVolume)
	a.Channel2.EnvelopeStepsInit = int32(envVolume)
	a.Channel2.EnvelopeSamples = int32(envSweep) * sampleRate / 64
	a.Channel2.EnvelopeIncreasing = envDirection == 1
}

// Start the 3rd sound channel.
func (a *APU) start3() {
	selection := (a.Memory[0x1E] & 0x40) >> 6 // 1 = stop when length in NR31 expires
	length := a.Memory[0x1B]

	duration := -1
	if selection == 1 {
		duration = int((256-float64(length))*(1/256)) * sampleRate
	}
	a.Channel3.Generator = Waveform(a.WaveformRam[:])
	a.Channel3.Reset(duration)
}

// Start the 4th sound channel.
func (a *APU) start4() {
	selection := (a.Memory[0x23] & 0x40) >> 6 // 1 = stop when length in NR44 expires
	length := a.Memory[0x20] & 0x3F

	// Envelope settings
	envVolume, envDirection, envSweep := a.extractEnvelope(a.Memory[0x21])

	duration := -1
	if selection == 1 {
		duration = int(float64(61-length)*(1/256)) * sampleRate
	}

	a.Channel4.Reset(duration)
	a.Channel4.EnvelopeSteps = int32(envVolume)
	a.Channel4.EnvelopeStepsInit = int32(envVolume)
	a.Channel4.EnvelopeSamples = int32(envSweep) * sampleRate / 64
	a.Channel4.EnvelopeIncreasing = envDirection == 1
}

// Extract some envelope variables from a byte.
func (a *APU) extractEnvelope(val byte) (volume, direction, sweep byte) {
	volume = (val & 0xF0) >> 4
	direction = (val & 0x8) >> 3 // 1 or 0
	sweep = val & 0x7
	return
}

var squareLimits = map[byte]float64{
	0: -0.25, // 12.5% ( _-------_-------_------- )
	1: -0.5,  // 25%   ( __------__------__------ )
	2: 0,     // 50%   ( ____----____----____---- ) (normal)
	3: 0.5,   // 75%   ( ______--______--______-- )
}

type WaveGeneratorType byte

const (
	squareWave WaveGeneratorType = iota
	noiseWave
	ramWave
)

type WaveGenerator struct {
	Type WaveGeneratorType
	Mod  float64
	Last float64
	Val  byte
}

func (g *WaveGenerator) At(apu *APU, t float64) byte {
	switch g.Type {
	case squareWave:
		if math.Sin(t) <= g.Mod {
			return 0xFF
		}
		return 0
	case noiseWave:
		if t-g.Last > twoPi {
			g.Last = t
			g.Val = byte(rand.Intn(2)) * 0xFF
		}
		return g.Val
	case ramWave:
		idx := int(math.Floor(t/twoPi*32)) % len(apu.WaveformRam)
		return apu.WaveformRam[idx]
	default:
		panic("unknown wave generator type")
	}
}

// Square returns a square wave generator with a given mod. This is used
// for channels 1 and 2.
func Square(mod float64) WaveGenerator {
	return WaveGenerator{
		Type: squareWave,
		Mod:  mod,
	}
}

// Noise returns a wave generator for a noise channel. This is used by
// channel 4.
func Noise() WaveGenerator {
	return WaveGenerator{Type: noiseWave}
}

// Waveform returns a wave generator for some waveform ram. This is used
// by channel 3.
func Waveform(ram []byte) WaveGenerator {
	return WaveGenerator{Type: ramWave}
}

// NewChannel returns a new sound channel using a sampling function.
func NewChannel() Channel {
	return Channel{}
}

// Channel represents one of four Gameboy sound channels.
type Channel struct {
	Frequency float64
	Generator WaveGenerator
	Time      float64
	Amplitude float64

	// Duration in samples
	Duration int32

	EnvelopeTime       int32
	EnvelopeSteps      int32
	EnvelopeStepsInit  int32
	EnvelopeSamples    int32
	EnvelopeIncreasing bool

	SweepTime     float64
	SweepStepLen  byte
	SweepSteps    byte
	SweepStep     byte
	SweepIncrease bool

	On bool
	// Debug flag to turn off sound output
	DebugOff bool
}

// Sample returns a single sample for streaming the sound output. Each sample
// will increase the internal timer based on the global sample rate.
func (chn *Channel) Sample(apu *APU) (output uint16) {
	step := chn.Frequency * twoPi / float64(sampleRate)
	chn.Time += step
	if chn.shouldPlay() && chn.On {
		// Take the sample value from the generator
		if !chn.DebugOff {
			output = uint16(float64(chn.Generator.At(apu, chn.Time)) * chn.Amplitude)
		}
		if chn.Duration > 0 {
			chn.Duration--
		}
	}
	chn.updateEnvelope()
	chn.updateSweep()
	return output
}

// Reset the channel to some default variables for the sweep, amplitude,
// envelope and duration.
func (chn *Channel) Reset(duration int) {
	chn.Amplitude = 1
	chn.EnvelopeTime = 0
	chn.SweepTime = 0
	chn.SweepStep = 0
	chn.Duration = int32(duration)
}

// Returns if the channel should be playing or not.
func (chn *Channel) shouldPlay() bool {
	return (chn.Duration == -1 || chn.Duration > 0) && chn.EnvelopeStepsInit > 0
}

// Update the state of the channels envelope.
func (chn *Channel) updateEnvelope() {
	if chn.EnvelopeSamples > 0 {
		chn.EnvelopeTime += 1
		if chn.EnvelopeSteps > 0 && chn.EnvelopeTime >= chn.EnvelopeSamples {
			chn.EnvelopeTime -= chn.EnvelopeSamples
			chn.EnvelopeSteps--
			if chn.EnvelopeSteps == 0 {
				chn.Amplitude = 0
			} else if chn.EnvelopeIncreasing {
				chn.Amplitude = 1 - float64(chn.EnvelopeSteps)/float64(chn.EnvelopeStepsInit)
			} else {
				chn.Amplitude = float64(chn.EnvelopeSteps) / float64(chn.EnvelopeStepsInit)
			}
		}
	}
}

var sweepTimes = map[byte]float64{
	1: 7.8 / 1000,
	2: 15.6 / 1000,
	3: 23.4 / 1000,
	4: 31.3 / 1000,
	5: 39.1 / 1000,
	6: 46.9 / 1000,
	7: 54.7 / 1000,
}

// Update the state of the channels sweep.
func (chn *Channel) updateSweep() {
	if chn.SweepStep < chn.SweepSteps {
		t := sweepTimes[chn.SweepStepLen]
		chn.SweepTime += perSample
		if chn.SweepTime > t {
			chn.SweepTime -= t
			chn.SweepStep += 1

			if chn.SweepIncrease {
				chn.Frequency += chn.Frequency / math.Pow(2, float64(chn.SweepStep))
			} else {
				chn.Frequency -= chn.Frequency / math.Pow(2, float64(chn.SweepStep))
			}
		}
	}
}
