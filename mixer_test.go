package mixer_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/dudk/mixer"
	"github.com/dudk/phono"
	"github.com/dudk/phono/mock"
	"github.com/dudk/phono/pipe"
	"github.com/dudk/wav"
)

const (
	wavPath1 = "_testdata/test1.wav"
	wavPath2 = "_testdata/test2.wav"
	outPath  = "_testdata/out.wav"
)

var (
	bufferSize  = phono.BufferSize(10)
	numChannels = phono.NumChannels(1)
	tests       = []struct {
		mock.Limit
		value1   float64
		value2   float64
		sum      float64
		messages uint64
		samples  uint64
	}{
		{
			Limit:    10,
			value1:   0.5,
			value2:   0.7,
			sum:      0.6,
			messages: 10,
			samples:  100,
		},
		{
			Limit:    1000,
			value1:   0.7,
			value2:   0.9,
			sum:      0.8,
			messages: 1000,
			samples:  10000,
		},
	}
)

func TestMixer(t *testing.T) {
	pump1 := &mock.Pump{
		Limit:       1,
		BufferSize:  bufferSize,
		NumChannels: numChannels,
	}
	pump2 := &mock.Pump{
		Limit:       1,
		BufferSize:  bufferSize,
		NumChannels: numChannels,
	}
	mix := mixer.New(bufferSize, numChannels)
	sink := &mock.Sink{}
	playback := pipe.New(
		pipe.WithName("Playback"),
		pipe.WithPump(mix),
		pipe.WithSinks(sink),
	)
	track1 := pipe.New(
		pipe.WithName("Track 1"),
		pipe.WithPump(pump1),
		pipe.WithSinks(mix),
	)
	track2 := pipe.New(
		pipe.WithName("Track 2"),
		pipe.WithPump(pump2),
		pipe.WithSinks(mix),
	)

	var err error
	for i, test := range tests {
		fmt.Printf("Test %v\n", i)
		track1.Push(phono.NewParams(
			pump1.LimitParam(test.Limit),
			pump1.ValueParam(test.value1),
		))
		track2.Push(phono.NewParams(
			pump2.LimitParam(test.Limit),
			pump2.ValueParam(test.value2),
		))

		_, err = pipe.Begin(track1.Run)
		assert.Nil(t, err)
		_, err = pipe.Begin(track2.Run)
		assert.Nil(t, err)
		_, err = pipe.Begin(playback.Run)
		assert.Nil(t, err)

		track1.Wait(pipe.Ready)
		track2.Wait(pipe.Ready)
		playback.Wait(pipe.Ready)
		for i := range sink.Buffer {
			for _, val := range sink.Buffer[i] {
				assert.Equal(t, test.sum, val)
			}
		}
		messageCount, sampleCount := sink.Count()
		assert.Equal(t, test.messages, messageCount)
		assert.Equal(t, test.samples, sampleCount)
	}

	track1.Close()
	track2.Close()
	playback.Close()
}

func TestWavMixer(t *testing.T) {
	bs := phono.BufferSize(512)

	p1, _ := wav.NewPump(wavPath1, bs)
	p2, _ := wav.NewPump(wavPath2, bs)

	s := wav.NewSink(outPath, p1.WavSampleRate(), p1.WavNumChannels(), p1.WavBitDepth(), p1.WavAudioFormat())

	m := mixer.New(bs, p1.WavNumChannels())

	track1 := pipe.New(
		pipe.WithPump(p1),
		pipe.WithSinks(m),
	)
	track2 := pipe.New(
		pipe.WithPump(p2),
		pipe.WithSinks(m),
	)

	playback := pipe.New(
		pipe.WithPump(m),
		pipe.WithSinks(s),
	)

	pipe.Begin(track1.Run)
	// defer track1.Close()
	pipe.Begin(track2.Run)
	// defer track2.Close()
	pipe.Begin(playback.Run)
	// defer playback.Close()

	track1.Wait(pipe.Ready)
	track2.Wait(pipe.Ready)
	playback.Wait(pipe.Ready)

	track1.Close()
	track2.Close()
	playback.Close()
}
