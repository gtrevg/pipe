package runner

import (
	"context"
	"errors"
	"sync"

	"github.com/dudk/phono"
	"github.com/dudk/phono/pipe"
)

// Pump represents pump's runner
type Pump struct {
	pipe.Pump
	Before BeforeAfterFunc
	After  BeforeAfterFunc
	out    chan *phono.Message
	*pipe.Metric
}

// Process represents processor's runner
type Process struct {
	pipe.Processor
	Before BeforeAfterFunc
	After  BeforeAfterFunc
	in     <-chan *phono.Message
	out    chan *phono.Message
	*pipe.Metric
}

// Sink represents sink's runner
type Sink struct {
	pipe.Sink
	Before BeforeAfterFunc
	After  BeforeAfterFunc
	in     <-chan *phono.Message
	*pipe.Metric
}

// BeforeAfterFunc represents setup/clean up functions which are executed on Run start and finish
type BeforeAfterFunc func() error

var (
	// ErrSingleUseReused is returned when object designed for single-use is being reused
	ErrSingleUseReused = errors.New("Error reuse single-use object")
)

const (
	// OutputCounter is a key for output counter within metric.
	// It calculates regular total output per component.
	OutputCounter = "Output"
)

// Run the Pump runner
func (p *Pump) Run(ctx context.Context, sampleRate phono.SampleRate, newMessage phono.NewMessageFunc) (<-chan *phono.Message, <-chan error, error) {
	p.Metric = pipe.NewMetric(sampleRate, OutputCounter)
	err := p.Before.call()
	if err != nil {
		return nil, nil, err
	}
	out := make(chan *phono.Message)
	errc := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errc)
		defer func() {
			err := p.After.call()
			if err != nil {
				errc <- err
			}
		}()
		defer p.Stop()
		for {
			m := newMessage()
			m.ApplyTo(p.Pump)
			m, err := p.Pump.Pump(m)
			if err != nil {
				if err != pipe.ErrEOP {
					errc <- err
				}
				return
			}
			p.Counters[OutputCounter].Advance(m.Buffer)
			select {
			case <-ctx.Done():
				return
			default:
				out <- m
			}
		}
	}()
	return out, errc, nil
}

// Run the Processor runner
func (p *Process) Run(sampleRate phono.SampleRate, in <-chan *phono.Message) (<-chan *phono.Message, <-chan error, error) {
	p.Metric = pipe.NewMetric(sampleRate, OutputCounter)
	err := p.Before.call()
	if err != nil {
		return nil, nil, err
	}
	errc := make(chan error, 1)
	p.in = in
	p.out = make(chan *phono.Message)
	go func() {
		defer close(p.out)
		defer close(errc)
		defer func() {
			err := p.After.call()
			if err != nil {
				errc <- err
			}
		}()
		defer p.Stop()
		for in != nil {
			select {
			case m, ok := <-in:
				if !ok {
					return
				}
				p.Counters[OutputCounter].Advance(m.Buffer)
				m.ApplyTo(p.Processor)
				m, err = p.Process(m)
				if err != nil {
					errc <- err
					return
				}
				p.out <- m
			}
		}
	}()
	return p.out, errc, nil
}

// Run the sink runner
func (s *Sink) Run(sampleRate phono.SampleRate, in <-chan *phono.Message) (<-chan error, error) {
	s.Metric = pipe.NewMetric(sampleRate, OutputCounter)
	err := s.Before.call()
	if err != nil {
		return nil, err
	}
	errc := make(chan error, 1)
	go func() {
		defer close(errc)
		defer func() {
			err := s.After.call()
			if err != nil {
				errc <- err
			}
		}()
		defer s.Stop()
		for in != nil {
			select {
			case m, ok := <-in:
				if !ok {
					return
				}
				s.Counters[OutputCounter].Advance(m.Buffer)
				m.Params.ApplyTo(s.Sink)
				err = s.Sink.Sink(m)
				if err != nil {
					errc <- err
					return
				}
			}
		}
	}()

	return errc, nil
}

// SingleUse is designed to be used in runner-return functions to define a single-use pipe elements
func SingleUse(once *sync.Once) (err error) {
	err = ErrSingleUseReused
	once.Do(func() {
		err = nil
	})
	return
}

func (fn BeforeAfterFunc) call() error {
	if fn == nil {
		return nil
	}
	return fn()
}
