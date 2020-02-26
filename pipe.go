package pipe

import (
	"context"
	"fmt"

	"pipelined.dev/signal"

	"pipelined.dev/pipe/internal/pool"
	"pipelined.dev/pipe/internal/runner"
	"pipelined.dev/pipe/metric"
	"pipelined.dev/pipe/mutate"
)

type (
	Bus struct {
		BufferSize int
		signal.SampleRate
		NumChannels int
	}
	// PumpMaker creates new pump structure for provided buffer size.
	// It pre-allocates all necessary buffers and structures.
	PumpMaker func(bufferSize int) (Pump, Bus, error)
	// Pump is a source of samples. Pump method accepts a new buffer and
	// fills it with signal data. If no data is available, io.EOF should
	// be returned. If pump cannot provide data to fulfill buffer, it can
	// trim the size of the buffer to align it with actual data.
	// Buffer size can only be decreased.
	Pump struct {
		mutate.Mutability
		Pump func(out signal.Float64) error
		Flush
	}

	// ProcessorMaker creates new pump structure for provided buffer size.
	// It pre-allocates all necessary buffers and structures.
	ProcessorMaker func(Bus) (Processor, Bus, error)
	// Processor defines interface for pipe processors. It receives two
	// buffers for input and output signal data. Buffer size could be
	// changed during execution, but only decrease allowed. Number of
	// channels cannot be changed.
	Processor struct {
		mutate.Mutability
		Process func(in, out signal.Float64) error
		Flush
	}

	// SinkMaker creates new pump structure for provided buffer size.
	// It pre-allocates all necessary buffers and structures.
	SinkMaker func(Bus) (Sink, error)
	// Sink is an interface for final stage in audio pipeline.
	// This components must not change buffer content. Route can have
	// multiple sinks and this will cause race condition.
	Sink struct {
		mutate.Mutability
		Sink func(in signal.Float64) error
		Flush
	}

	// Flush provides a hook to flush all buffers for the component.
	Flush func(context.Context) error
)

type (
	// Route is a sequence of DSP components.
	Route struct {
		numChannels int
		mutators    chan mutate.Mutators
		receivers   map[mutate.Mutability]struct{}
		pump        runner.Pump
		processors  []runner.Processor
		sink        runner.Sink
	}

	// Line defines sequence of components closures.
	// It has a single pump, zero or many processors, executed
	// sequentially and one or many sinks executed in parallel.
	Line struct {
		Pump       PumpMaker
		Processors []ProcessorMaker
		Sink       SinkMaker
	}

	// Pipe controls the execution of multiple chained lines. Lines might be chained
	// through components, mixer for example.  If lines are not chained, they must be
	// controlled by separate Pipes. Use New constructor to instantiate new Pipes.
	Pipe struct {
		mutability mutate.Mutability
		ctx        context.Context
		cancelFn   context.CancelFunc
		merger     *merger
		routes     []Route
		mutators   map[chan mutate.Mutators]mutate.Mutators
		pull       chan chan mutate.Mutators
		push       chan []mutate.Mutation
		errors     chan error
	}
)

// Route line components. All closures are executed and wrapped into runners.
func (l Line) Route(bufferSize int) (Route, error) {
	receivers := make(map[mutate.Mutability]struct{})
	pump, input, err := l.Pump.runner(bufferSize)
	if err != nil {
		return Route{}, fmt.Errorf("error routing %w", err)
	}
	receivers[pump.Mutability] = struct{}{}

	var (
		processors []runner.Processor
		processor  runner.Processor
	)
	for _, fn := range l.Processors {
		processor, input, err = fn.runner(bufferSize, input)
		if err != nil {
			return Route{}, fmt.Errorf("error routing %w", err)
		}
		processors = append(processors, processor)
		receivers[processor.Mutability] = struct{}{}
	}

	sink, err := l.Sink.runner(bufferSize, input)
	if err != nil {
		return Route{}, fmt.Errorf("error routing sink: %w", err)
	}
	receivers[sink.Mutability] = struct{}{}

	return Route{
		mutators:   make(chan mutate.Mutators),
		receivers:  receivers,
		pump:       pump,
		processors: processors,
		sink:       sink,
	}, nil
}

func (fn PumpMaker) runner(bufferSize int) (runner.Pump, Bus, error) {
	pump, bus, err := fn(bufferSize)
	if err != nil {
		return runner.Pump{}, Bus{}, fmt.Errorf("pump: %w", err)
	}
	return runner.Pump{
		Mutability: pump.Mutable(),
		Output:     pool.Get(bufferSize, bus.NumChannels),
		Fn:         pump.Pump,
		Flush:      runner.Flush(pump.Flush),
		Meter:      metric.Meter(pump, bus.SampleRate),
	}, bus, nil
}

func (fn ProcessorMaker) runner(bufferSize int, input Bus) (runner.Processor, Bus, error) {
	processor, output, err := fn(input)
	if err != nil {
		return runner.Processor{}, Bus{}, fmt.Errorf("processor: %w", err)
	}
	return runner.Processor{
		Mutability: processor.Mutable(),
		Input:      pool.Get(bufferSize, input.NumChannels),
		Output:     pool.Get(bufferSize, output.NumChannels),
		Fn:         processor.Process,
		Flush:      runner.Flush(processor.Flush),
		Meter:      metric.Meter(processor, output.SampleRate),
	}, output, nil
}

func (fn SinkMaker) runner(bufferSize int, input Bus) (runner.Sink, error) {
	sink, err := fn(input)
	if err != nil {
		return runner.Sink{}, fmt.Errorf("sink: %w", err)
	}
	return runner.Sink{
		Mutability: sink.Mutable(),
		Input:      pool.Get(bufferSize, input.NumChannels),
		Fn:         sink.Sink,
		Flush:      runner.Flush(sink.Flush),
		Meter:      metric.Meter(sink, input.SampleRate),
	}, nil
}

// New creates a new pipeline.
// Returned pipeline is in Ready state.
func New(ctx context.Context, options ...Option) Pipe {
	ctx, cancelFn := context.WithCancel(ctx)
	p := Pipe{
		mutability: mutate.Mutable(),
		merger: &merger{
			errors: make(chan error, 1),
		},
		ctx:      ctx,
		cancelFn: cancelFn,
		mutators: make(map[chan mutate.Mutators]mutate.Mutators),
		routes:   make([]Route, 0),
		pull:     make(chan chan mutate.Mutators),
		push:     make(chan []mutate.Mutation, 1),
		errors:   make(chan error, 1),
	}
	for _, option := range options {
		option(&p)
	}
	if len(p.routes) == 0 {
		panic("pipe without routes")
	}
	// options are before this step
	p.merger.merge(start(p.ctx, p.pull, p.routes)...)
	go p.merger.wait()
	go func() {
		var (
			puller chan mutate.Mutators
		)
		defer close(p.errors)
		for {
			select {
			case mutations := <-p.push:
				for _, m := range mutations {
					// mutate pipe itself
					if m.Mutability == p.mutability {
						if err := m.Mutator(); err != nil {
							p.interrupt(err)
						}
					} else {
						if puller = p.getPuller(m.Mutability); puller != nil {
							p.mutators[puller] = p.mutators[puller].Add(m.Mutability, m.Mutator)
						}
					}
				}
			case puller = <-p.pull:
				mutators := p.mutators[puller]
				p.mutators[puller] = nil
				puller <- mutators
				continue
			case err, ok := <-p.merger.errors:
				// merger has buffer of one error,
				// if more errors happen, they will be ignored.
				if ok {
					p.interrupt(err)
				}
				return
			}
		}
	}()
	return p
}

func (p Pipe) interrupt(err error) {
	p.cancelFn()
	// wait until all groutines stop.
	for {
		// only the first error is propagated.
		if _, ok := <-p.merger.errors; !ok {
			break
		}
	}
	p.errors <- fmt.Errorf("pipe error: %w", err)
}

// start starts the execution of pipe.
func start(ctx context.Context, pull chan<- chan mutate.Mutators, routes []Route) []<-chan error {
	// start all runners
	// error channel for each component
	errChans := make([]<-chan error, 0, 2*len(routes))
	for _, r := range routes {
		errChans = append(errChans, r.start(ctx, pull)...)
	}
	return errChans
}

func (r Route) start(ctx context.Context, pull chan<- chan mutate.Mutators) []<-chan error {
	errChans := make([]<-chan error, 0, 2+len(r.processors))
	// start pump
	out, errs := r.pump.Run(ctx, pull, r.mutators)
	errChans = append(errChans, errs)

	// start chained processesing
	for _, proc := range r.processors {
		out, errs = proc.Run(ctx, out)
		errChans = append(errChans, errs)
	}

	errs = r.sink.Run(ctx, out)
	errChans = append(errChans, errs)
	return errChans
}

// Push new mutators into pipe.
// Calling this method after pipe is done will cause a panic.
func (p Pipe) Push(mutations ...mutate.Mutation) {
	p.push <- mutations
}

func (p Pipe) getPuller(id mutate.Mutability) chan mutate.Mutators {
	for _, route := range p.routes {
		if _, ok := route.receivers[id]; ok {
			return route.mutators
		}
	}
	return nil
}

func (p Pipe) AddRoute(r Route) mutate.Mutation {
	return mutate.Mutation{
		Mutability: p.mutability,
		Mutator: func() error {
			p.merger.merge(r.start(p.ctx, p.pull)...)
			return nil
		},
	}
}

// Processors is a helper function to use in line constructors.
func Processors(processors ...ProcessorMaker) []ProcessorMaker {
	return processors
}

// Wait for state transition or first error to occur.
func (p Pipe) Wait() error {
	for err := range p.errors {
		if err != nil {
			return err
		}
	}
	return nil
}
