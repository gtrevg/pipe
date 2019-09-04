package pipe_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/goleak"

	"github.com/pipelined/mock"
	"github.com/pipelined/pipe"
	"github.com/pipelined/signal"
)

const (
	bufferSize = 512
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestLine(t *testing.T) {
	pump := &mock.Pump{
		Limit:       100 * bufferSize,
		NumChannels: 1,
	}
	proc1 := &mock.Processor{}
	proc2 := &mock.Processor{}
	sink1 := &mock.Sink{}
	sink2 := &mock.Sink{}

	l, err := pipe.Line(
		&pipe.Pipe{
			Pump:       pump,
			Processors: pipe.Processors(proc1, proc2),
			Sinks:      pipe.Sinks(sink1, sink2),
		},
	)
	assert.Nil(t, err)

	// start
	runc := l.Run(context.Background(), bufferSize)
	assert.NotNil(t, runc)
	assert.Nil(t, err)

	// test params push
	pumpID, ok := l.ComponentID(pump)
	assert.True(t, ok)
	assert.NotEmpty(t, pumpID)
	// push new limit for pump
	newLimit := 200
	paramFn := pump.LimitParam(newLimit)
	l.Push(pumpID, paramFn)

	// pause
	err = pipe.Wait(l.Pause())
	assert.Nil(t, err)
	// runc must be cancelled by now
	err = pipe.Wait(runc)
	assert.Nil(t, err)

	// resume
	err = pipe.Wait(l.Resume())
	assert.Nil(t, err)

	pipe.Wait(l.Close())
}

func TestSampleRate(t *testing.T) {
	var p *pipe.Pipe
	assert.Equal(t, signal.SampleRate(0), p.SampleRate())

	p = &pipe.Pipe{}
	assert.Equal(t, signal.SampleRate(0), p.SampleRate())
}
