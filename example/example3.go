package example

import (
	"github.com/dudk/phono"
	"github.com/dudk/phono/mixer"
	"github.com/dudk/phono/pipe"
	"github.com/dudk/phono/wav"
)

// Example 3:
//		Read two .wav files
//		Mix them
//		Save result into new .wav file
//
// NOTE: For example both wav files have same characteristics i.e: sample rate, bit depth and number of channels.
// In real life implicit conversion will be needed.
func three() {
	bs := phono.BufferSize(512)
	inPath1 := "../_testdata/sample1.wav"
	inPath2 := "../_testdata/sample2.wav"
	outPath := "../_testdata/out/example3.wav"

	wavPump1, err := wav.NewPump(inPath1, bs)
	check(err)
	wavPump2, err := wav.NewPump(inPath2, bs)
	check(err)

	wavSink, err := wav.NewSink(
		outPath,
		wavPump1.WavSampleRate(),
		wavPump1.WavNumChannels(),
		wavPump1.WavBitDepth(),
		wavPump1.WavAudioFormat(),
	)
	check(err)
	mixer := mixer.New(bs, wavPump1.WavNumChannels())

	track1 := pipe.New(
		pipe.WithPump(wavPump1),
		pipe.WithSinks(mixer),
	)
	defer track1.Close()
	track2 := pipe.New(
		pipe.WithPump(wavPump2),
		pipe.WithSinks(mixer),
	)
	defer track2.Close()
	out := pipe.New(
		pipe.WithPump(mixer),
		pipe.WithSinks(wavSink),
	)
	defer out.Close()

	track1Done, err := track1.Begin(pipe.Run)
	check(err)
	track2Done, err := track2.Begin(pipe.Run)
	check(err)
	outDone, err := out.Begin(pipe.Run)
	check(err)

	err = track1.Wait(track1Done)
	check(err)
	err = track2.Wait(track2Done)
	check(err)
	err = out.Wait(outDone)
	check(err)
}
