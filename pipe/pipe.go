package pipe

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/dudk/phono"
	"github.com/dudk/phono/log"
	"github.com/rs/xid"
)

// Pump is a source of samples
type Pump interface {
	phono.Identifiable
	RunPump(sourceID string) PumpRunner
	Pump(*phono.Message) (*phono.Message, error)
}

// Processor defines interface for pipe-processors
type Processor interface {
	phono.Identifiable
	RunProcess(sourceID string) ProcessRunner
	Process(*phono.Message) (*phono.Message, error)
}

// Sink is an interface for final stage in audio pipeline
type Sink interface {
	phono.Identifiable
	RunSink(sourceID string) SinkRunner
	Sink(*phono.Message) error
}

// PumpRunner is a pump runner
type PumpRunner interface {
	phono.Identifiable
	Run(context.Context, phono.NewMessageFunc) (<-chan *phono.Message, <-chan error, error)
}

// ProcessRunner is a processor runner
type ProcessRunner interface {
	phono.Identifiable
	Run(<-chan *phono.Message) (<-chan *phono.Message, <-chan error, error)
}

// SinkRunner is a sink runner
type SinkRunner interface {
	phono.Identifiable
	Run(in <-chan *phono.Message) (errc <-chan error, err error)
}

// Pipe is a pipeline with fully defined sound processing sequence
// it has:
//	 1 		pump
//	 0..n 	processors
//	 1..n	sinks
type Pipe struct {
	name string
	phono.UID
	cancelFn   context.CancelFunc
	pump       PumpRunner
	processors []ProcessRunner
	sinks      []SinkRunner

	cachedParams *phono.Params
	// errors channel
	errc chan error
	// event channel
	eventc chan eventMessage
	// signals channel
	signalc chan signalMessage

	message struct {
		ask  chan struct{}
		take chan *phono.Message
	}

	log log.Logger
}

// Param provides a way to set parameters to pipe
// returns phono.ParamFunc, which can be executed later
type Param func(p *Pipe) phono.ParamFunc

// stateFn is the state function for pipe state machine
type stateFn func(p *Pipe) stateFn

// actionFn is an action function which causes a pipe state change
// chan error is closed when state is changed
type actionFn func(p *Pipe) (chan error, Signal)

// event is sent when user does some action
type event int

type eventMessage struct {
	event
	done   chan error
	params *phono.Params
}

// Signal is sent when pipe changes the state
type Signal int

type signalMessage struct {
	Signal
	err error
}

const (
	run event = iota
	pause
	resume
	params

	// Ready signal is sent when pipe goes to ready state
	Ready Signal = iota
	// Running signal is sent when pipe goes to running state
	Running
	// Paused signal is sent when pipe goes to paused state
	Paused
	// Pausing signal is sent when pipe goes to pausing
	Pausing
)

var (
	// ErrInvalidState is returned if pipe method cannot be executed at this moment
	ErrInvalidState = errors.New("Invalid state")
	// ErrEOP is returned if pump finished processing and indicates a gracefull ending
	ErrEOP = errors.New("End of pipe")
)

// New creates a new pipe and applies provided params
// returned pipe is in ready state
func New(params ...Param) *Pipe {
	p := &Pipe{
		processors: make([]ProcessRunner, 0),
		sinks:      make([]SinkRunner, 0),
		eventc:     make(chan eventMessage),
		signalc:    make(chan signalMessage, 100),
		log:        log.GetLogger(),
	}
	p.SetID(xid.New().String())
	for _, param := range params {
		param(p)()
	}
	state := stateFn(ready)
	go func() {
		for state != nil {
			state = state(p)
		}
		p.stop()
	}()
	p.Wait(Ready)
	return p
}

// WithName sets name to Pipe
func WithName(n string) Param {
	return func(p *Pipe) phono.ParamFunc {
		return func() {
			p.name = n
		}
	}
}

// WithPump sets pump to Pipe
func WithPump(pump Pump) Param {
	if pump.ID() == "" {
		pump.SetID(xid.New().String())
	}
	return func(p *Pipe) phono.ParamFunc {
		return func() {
			p.pump = pump.RunPump(p.ID())
		}
	}
}

// WithProcessors sets processors to Pipe
func WithProcessors(processors ...Processor) Param {
	for i := range processors {
		if processors[i].ID() == "" {
			processors[i].SetID(xid.New().String())
		}
	}
	return func(p *Pipe) phono.ParamFunc {
		return func() {
			for i := range processors {
				p.processors = append(p.processors, processors[i].RunProcess(p.ID()))
			}
		}
	}
}

// WithSinks sets sinks to Pipe
func WithSinks(sinks ...Sink) Param {
	for i := range sinks {
		if sinks[i].ID() == "" {
			sinks[i].SetID(xid.New().String())
		}
	}
	return func(p *Pipe) phono.ParamFunc {
		return func() {
			for i := range sinks {
				p.sinks = append(p.sinks, sinks[i].RunSink(p.ID()))
			}
		}
	}
}

// Run switches pipe into running state
func Run(p *Pipe) (chan error, Signal) {
	runEvent := eventMessage{
		event: run,
		done:  make(chan error),
	}
	p.eventc <- runEvent
	return runEvent.done, Ready
}

// Pause switches pipe into pausing state
func Pause(p *Pipe) (chan error, Signal) {
	pauseEvent := eventMessage{
		event: pause,
		done:  make(chan error),
	}
	p.eventc <- pauseEvent
	return pauseEvent.done, Paused
}

// Resume switches pipe into running state
func Resume(p *Pipe) (chan error, Signal) {
	resumeEvent := eventMessage{
		event: resume,
		done:  make(chan error),
	}
	p.eventc <- resumeEvent
	return resumeEvent.done, Ready
}

// Wait for signal or first error to occur
func (p *Pipe) Wait(s Signal) error {
	for msg := range p.signalc {
		if msg.err != nil {
			p.log.Debug(fmt.Sprintf("%v received signal: %v with error: %v", p, msg.Signal, msg.err))
			return msg.err
		}
		if msg.Signal == s {
			p.log.Debug(fmt.Sprintf("%v received signal: %v", p, msg.Signal))
			return nil
		}
	}
	return nil
}

// WaitAsync allows to wait for signal or first error occurance through the returned channel
func (p *Pipe) WaitAsync(s Signal) <-chan error {
	errc := make(chan error)
	go func() {
		err := p.Wait(s)
		if err != nil {
			errc <- err
		} else {
			close(errc)
		}
	}()
	return errc
}

// Push new params into pipe
func (p *Pipe) Push(newParams *phono.Params) {
	p.eventc <- eventMessage{
		event:  params,
		params: newParams,
	}
}

// Close must be called to clean up pipe's resources
func (p *Pipe) Close() {
	if p.cancelFn != nil {
		p.cancelFn()
	}
	close(p.eventc)
}

// Begin executes a passed action and waits till first error or till the passed function receive a done signal
func (p *Pipe) Begin(fn actionFn) (Signal, error) {
	errc, sig := fn(p)
	if errc == nil {
		return sig, nil
	}
	for err := range errc {
		if err != nil {
			return sig, err
		}
	}
	return sig, nil
}

// Do begins an action and waits for returned signal
func (p *Pipe) Do(fn actionFn) error {
	errc, sig := fn(p)
	if errc == nil {
		return p.Wait(sig)
	}
	for err := range errc {
		if err != nil {
			return err
		}
	}
	return p.Wait(sig)
}

// merge error channels
func mergeErrors(errcList ...<-chan error) chan error {
	var wg sync.WaitGroup
	out := make(chan error, len(errcList))

	//function to wait for error channel
	output := func(c <-chan error) {
		for n := range c {
			out <- n
		}
		wg.Done()
	}
	wg.Add(len(errcList))
	for _, e := range errcList {
		go output(e)
	}

	//wait and close out
	go func() {
		wg.Wait()
		close(out)
	}()

	return out
}

func (p *Pipe) broadcastToSinks(in <-chan *phono.Message) ([]<-chan error, error) {
	//init errcList for sinks error channels
	errcList := make([]<-chan error, 0, len(p.sinks))
	//list of channels for broadcast
	broadcasts := make([]chan *phono.Message, len(p.sinks))
	for i := range broadcasts {
		broadcasts[i] = make(chan *phono.Message)
	}

	//start broadcast
	for i, s := range p.sinks {
		errc, err := s.Run(broadcasts[i])
		if err != nil {
			p.log.Debug(fmt.Sprintf("%v failed to start sink %v error: %v", p, s.ID(), err))
			return nil, err
		}
		errcList = append(errcList, errc)
	}

	go func() {
		//close broadcasts on return
		defer func() {
			for i := range broadcasts {
				close(broadcasts[i])
			}
		}()
		for buf := range in {
			for i := range broadcasts {
				broadcasts[i] <- buf
			}
		}
	}()

	return errcList, nil
}

// soure returns a default message producer which caches params
// if new params are pushed into pipe - next message will contain them
// if pipe paused this call will block
func (p *Pipe) soure() phono.NewMessageFunc {
	p.message.ask = make(chan struct{})
	p.message.take = make(chan *phono.Message)
	var do struct{}
	return func() *phono.Message {
		p.message.ask <- do
		msg := <-p.message.take
		msg.SourceID = p.ID()
		return msg
	}
}

// ready state defines that pipe can:
// 	run - start processing
func ready(p *Pipe) stateFn {
	p.log.Debug(fmt.Sprintf("%v is ready", p))
	p.signalc <- signalMessage{Ready, nil}
	for {
		select {
		case e, ok := <-p.eventc:
			if !ok {
				return nil
			}
			p.log.Debug(fmt.Sprintf("%v got new event: %v", p, e))
			switch e.event {
			case params:
				e.params.ApplyTo(p)
				p.cachedParams = p.cachedParams.Merge(e.params)
			case run:
				ctx, cancelFn := context.WithCancel(context.Background())
				p.cancelFn = cancelFn
				errcList := make([]<-chan error, 0, 1+len(p.processors)+len(p.sinks))

				// start pump
				// pumpRunner := p.pump.RunPump(p.ID())
				out, errc, err := p.pump.Run(ctx, p.soure())
				if err != nil {
					p.log.Debug(fmt.Sprintf("%v failed to start pump %v error: %v", p, p.pump.ID(), err))
					e.done <- err
					p.cancelFn()
					return ready
				}
				errcList = append(errcList, errc)

				// start chained processesing
				for _, proc := range p.processors {
					out, errc, err = proc.Run(out)
					if err != nil {
						p.log.Debug(fmt.Sprintf("%v failed to start processor %v error: %v", p, proc.ID(), err))
						e.done <- err
						p.cancelFn()
						return ready
					}
					errcList = append(errcList, errc)
				}

				sinkErrcList, err := p.broadcastToSinks(out)
				if err != nil {
					e.done <- err
					p.cancelFn()
					return ready
				}
				errcList = append(errcList, sinkErrcList...)
				p.errc = mergeErrors(errcList...)
				close(e.done)
				return running
			default:
				e.done <- ErrInvalidState
			}
		}
	}
}

// running state defines that pipe can be:
//	paused - pause the processing
func running(p *Pipe) stateFn {
	p.log.Debug(fmt.Sprintf("%v is running", p))
	p.signalc <- signalMessage{Running, nil}
	for {
		select {
		case <-p.message.ask:
			message := new(phono.Message)
			if !p.cachedParams.Empty() {
				message.Params = p.cachedParams
				p.cachedParams = &phono.Params{}
			}
			p.message.take <- message
		case err := <-p.errc:
			if err != nil {
				p.signalc <- signalMessage{Running, err}
				p.cancelFn()
			}
			return ready
		case e, ok := <-p.eventc:
			if !ok {
				return nil
			}
			p.log.Debug(fmt.Sprintf("%v got new event: %v", p, e))
			switch e.event {
			case params:
				e.params.ApplyTo(p)
				p.cachedParams = p.cachedParams.Merge(e.params)
			case pause:
				close(e.done)
				return pausing
			default:
				e.done <- ErrInvalidState
			}
		}
	}
}

// pausing defines a state when pipe accepted a pause command and pushed message with confirmation
func pausing(p *Pipe) stateFn {
	p.log.Debug(fmt.Sprintf("%v is pausing", p))
	for {
		select {
		case <-p.message.ask:
			message := new(phono.Message)
			if !p.cachedParams.Empty() {
				message.Params = p.cachedParams
				p.cachedParams = &phono.Params{}
			}
			var wg sync.WaitGroup
			wg.Add(len(p.sinks))
			for _, sink := range p.sinks {
				param := phono.ReceivedBy(&wg, sink)
				message.Params = message.Params.Add(param)
			}
			p.message.take <- message
			wg.Wait()
			return paused
		case err := <-p.errc:
			// if nil error is received, it means that pipe finished before pause got finished
			if err != nil {
				p.signalc <- signalMessage{Pausing, err}
				p.cancelFn()
			} else {
				// because pipe is finished, we need send this signal to stop waiting
				p.signalc <- signalMessage{Paused, nil}
			}
			return ready
		case e, ok := <-p.eventc:
			if !ok {
				return nil
			}
			p.log.Debug(fmt.Sprintf("%v got new event: %v", p, e))
			switch e.event {
			case params:
				e.params.ApplyTo(p)
				p.cachedParams = p.cachedParams.Merge(e.params)
			default:
				e.done <- ErrInvalidState
			}
		}
	}
}

func paused(p *Pipe) stateFn {
	p.log.Debug(fmt.Sprintf("%v is paused", p))
	p.signalc <- signalMessage{Paused, nil}
	for {
		select {
		case e, ok := <-p.eventc:
			if !ok {
				return nil
			}
			p.log.Debug(fmt.Sprintf("%v got new event: %v", p, e))
			switch e.event {
			case params:
				e.params.ApplyTo(p)
				p.cachedParams = p.cachedParams.Merge(e.params)
			case resume:
				close(e.done)
				return running
			default:
				e.done <- ErrInvalidState
			}
		}
	}
}

// stop the pipe and clean up resources
func (p *Pipe) stop() {
	close(p.message.ask)
	close(p.message.take)
}

// Convert the event to a string
func (e event) String() string {
	switch e {
	case run:
		return "run"
	case pause:
		return "pause"
	case resume:
		return "resume"
	case params:
		return "params"
	}
	return "unknown"
}

// Convert the event to a string
func (s Signal) String() string {
	switch s {
	case Ready:
		return "ready"
	case Running:
		return "running"
	case Paused:
		return "paused"
	case Pausing:
		return "pausing"
	}
	return "unknown"
}

// Convert pipe to string. If name is included if has value
func (p *Pipe) String() string {
	if p.name == "" {
		return p.ID()
	}
	return fmt.Sprintf("%v %v", p.name, p.ID())
}
