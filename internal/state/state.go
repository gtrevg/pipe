package state

import (
	"errors"
	"sync"
)

var (
	// ErrInvalidState is returned if pipe method cannot be executed at this moment.
	ErrInvalidState = errors.New("invalid state")
)

// Handle manages the lifecycle of the pipe.
type Handle struct {
	// event channel used to handle new events for state machine.
	// created in custructor, never closed.
	eventc chan eventMessage
	// ask for new message request for the chain.
	// created in custructor, never closed.
	givec chan string
	// errc used to fan-in errors from components.
	// created in run event, closed when all components are done.
	merger
	// cancel the net execution.
	// created in run event, closed on cancel event or when error is recieved.
	cancelc      chan struct{}
	startFn      StartFunc
	newMessageFn NewMessageFunc
	pushParamsFn PushParamsFunc
}

// merger fans-in error channels.
type merger struct {
	wg   *sync.WaitGroup
	errc chan error
}

// StartFunc is the closure to trigger the start of a pipe.
type StartFunc func(bufferSize int, cancelc chan struct{}, givec chan<- string) []<-chan error

// NewMessageFunc is the closure to send a message into a pipe.
type NewMessageFunc func(pipeID string)

// PushParamsFunc is the closure to push new params into pipe.
type PushParamsFunc func(componentID string, params Params)

// state identifies one of the possible states pipe can be in.
type state interface {
	listen(*Handle, target) (state, target)
	transition(*Handle, eventMessage) (state, error)
}

// idleState identifies that the pipe is ONLY waiting for user to send an event.
type idleState interface {
	state
}

// activeState identifies that the pipe is processing signals and also is waiting for user to send an event.
type activeState interface {
	state
	sendMessage(h *Handle, pipeID string) state
}

// states
type (
	ready   struct{}
	running struct{}
	paused  struct{}
)

// states variables
var (
	Ready   ready   // Ready means that pipe can be started.
	Running running // Running means that pipe is executing at the moment.
	Paused  paused  // Paused means that pipe is paused and can be resumed.
)

// event identifies the type of event
type event int

// eventMessage is passed into pipe's event channel when user does some action.
type eventMessage struct {
	event              // event type.
	Params             // new Params.
	componentID string // ids of components which need to be called.
	target
	bufferSize int
}

// target identifies which state is expected from pipe.
type target struct {
	state idleState  // end state for this event.
	errc  chan error // channel to send errors. it's closed when target state is reached.
}

// types of events.
const (
	run event = iota
	pause
	resume
	push
	cancel
)

// NewHandle returns new initalized handle that can be used to manage lifecycle.
func NewHandle(start StartFunc, newMessage NewMessageFunc, pushParams PushParamsFunc) *Handle {
	h := Handle{
		eventc:       make(chan eventMessage, 1),
		givec:        make(chan string),
		startFn:      start,
		newMessageFn: newMessage,
		pushParamsFn: pushParams,
	}
	return &h
}

// Run sends a run event into handle.
// Calling this method after handle is closed causes a panic.
func (h *Handle) Run(bufferSize int) chan error {
	runEvent := eventMessage{
		event:      run,
		bufferSize: bufferSize,
		target: target{
			state: Ready,
			errc:  make(chan error, 1),
		},
	}
	h.eventc <- runEvent
	return runEvent.target.errc
}

// Pause sends a pause event into handle.
// Calling this method after handle is closed causes a panic.
func (h *Handle) Pause() chan error {
	pauseEvent := eventMessage{
		event: pause,
		target: target{
			state: Paused,
			errc:  make(chan error, 1),
		},
	}
	h.eventc <- pauseEvent
	return pauseEvent.target.errc
}

// Resume sends a resume event into handle.
// Calling this method after handle is closed causes a panic.
func (h *Handle) Resume() chan error {
	resumeEvent := eventMessage{
		event: resume,
		target: target{
			state: Ready,
			errc:  make(chan error, 1),
		},
	}
	h.eventc <- resumeEvent
	return resumeEvent.target.errc
}

// Close must be called to clean up handle's resources.
func (h *Handle) Close() chan error {
	cancelEvent := eventMessage{
		event: cancel,
		target: target{
			state: nil,
			errc:  make(chan error, 1),
		},
	}
	h.eventc <- cancelEvent
	return cancelEvent.target.errc
}

// Push new params into pipe.
// Calling this method after pipe is closed causes a panic.
func (h *Handle) Push(component interface{}, paramFuncs ...func()) {
	var componentID string
	params := Params(make(map[string][]func()))
	h.eventc <- eventMessage{
		componentID: componentID,
		event:       push,
		Params:      params.Add(componentID, paramFuncs...),
	}
}

// Loop listens until nil state is returned.
func Loop(h *Handle, s state) {
	t := target{}
	for s != nil {
		s, t = s.listen(h, t)
	}
	// cancel last pending target
	t.dismiss()
	close(h.eventc)
}

// idle is used to listen to handle's channels which are relevant for idle state.
// s is the new state, t is the target state and d channel to notify target transition.
func (h *Handle) idle(s idleState, t target) (state, target) {
	if s == t.state || s == Ready {
		t = t.dismiss()
	}
	for {
		var newState state
		var err error
		select {
		case e := <-h.eventc:
			newState, err = s.transition(h, e)
			if err != nil {
				e.target.handle(err)
			} else if e.hasTarget() {
				t.dismiss()
				t = e.target
			}
		}
		if s != newState {
			return newState, t
		}
	}
}

// active is used to listen to handle's channels which are relevant for active state.
func (h *Handle) active(s activeState, t target) (state, target) {
	for {
		var newState state
		var err error
		select {
		case e := <-h.eventc:
			newState, err = s.transition(h, e)
			if err != nil {
				e.target.handle(err)
			} else if e.hasTarget() {
				t.dismiss()
				t = e.target
			}
		case pipeID := <-h.givec:
			newState = s.sendMessage(h, pipeID)
		case err, ok := <-h.errc:
			if ok {
				close(h.cancelc)
				t.handle(err)
			}
			return Ready, t
		}
		if s != newState {
			return newState, t
		}
	}
}

func (s ready) listen(h *Handle, t target) (state, target) {
	return h.idle(s, t)
}

func (s ready) transition(h *Handle, e eventMessage) (state, error) {
	switch e.event {
	case cancel:
		return nil, nil
	case push:
		h.pushParamsFn("", e.Params)
		return s, nil
	case run:
		h.cancelc = make(chan struct{})
		h.merger = mergeErrors(h.startFn(e.bufferSize, h.cancelc, h.givec))
		return Running, nil
	}
	return s, ErrInvalidState
}

func (s running) listen(h *Handle, t target) (state, target) {
	return h.active(s, t)
}

func (s running) transition(h *Handle, e eventMessage) (state, error) {
	switch e.event {
	case cancel:
		close(h.cancelc)
		err := wait(h.errc)
		return nil, err
	case push:
		h.pushParamsFn("", e.Params)
		return s, nil
	case pause:
		return Paused, nil
	}
	return s, ErrInvalidState
}

func (s running) sendMessage(h *Handle, pipeID string) state {
	h.newMessageFn(pipeID)
	return s
}

func (s paused) listen(h *Handle, t target) (state, target) {
	return h.idle(s, t)
}

func (s paused) transition(h *Handle, e eventMessage) (state, error) {
	switch e.event {
	case cancel:
		close(h.cancelc)
		err := wait(h.errc)
		return nil, err
	case push:
		h.pushParamsFn("", e.Params)
		return s, nil
	case resume:
		return Running, nil
	}
	return s, ErrInvalidState
}

// hasTarget checks if event contaions target.
func (e eventMessage) hasTarget() bool {
	return e.target.errc != nil
}

// reach closes error channel and cancel waiting of target.
func (t target) dismiss() target {
	if t.errc != nil {
		t.state = nil
		close(t.errc)
		t.errc = nil
	}
	return t
}

// handleError pushes error into target. panic happens if no target defined.
func (t target) handle(err error) {
	if t.errc != nil {
		t.errc <- err
	} else {
		panic(err)
	}
}

// receivedBy returns channel which is closed when param received by identified entity
func receivedBy(wg *sync.WaitGroup, id string) func() {
	return func() {
		wg.Done()
	}
}

func wait(d <-chan error) error {
	for err := range d {
		if err != nil {
			return err
		}
	}
	return nil
}

// Convert the event to a string.
func (e event) String() string {
	switch e {
	case run:
		return "run"
	case pause:
		return "pause"
	case resume:
		return "resume"
	case push:
		return "params"
	}
	return "unknown"
}

// merge error channels from all components into one.
func mergeErrors(errcList []<-chan error) merger {
	m := merger{
		wg:   &sync.WaitGroup{},
		errc: make(chan error, 1),
	}

	//function to wait for error channel
	m.wg.Add(len(errcList))
	for _, ec := range errcList {
		go m.done(ec)
	}

	//wait and close out
	go func() {
		m.wg.Wait()
		close(m.errc)
	}()
	return m
}

func (m merger) done(ec <-chan error) {
	// block until error is received or channel is closed
	if err, ok := <-ec; ok {
		select {
		case m.errc <- err:
		default:
		}
	}
	m.wg.Done()
}
