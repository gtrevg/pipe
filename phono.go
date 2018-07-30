package phono

import (
	"fmt"
	"sync"
)

// Pipe transport types
type (
	// Buffer represent a sample data sliced per channel
	Buffer [][]float64

	// Frame represents a segment of buffer
	// Start is a start position of segment
	// Len is a length of segment
	//
	// Frame is not a copy of a buffer
	Frame struct {
		Buffer
		Start int64
		Len   int
	}

	// Message is a main structure for pipe transport
	Message struct {
		// Buffer of message
		Buffer
		// Params for pipe
		*Params
		// ID of original pipe
		SourceID string
	}

	// NewMessageFunc is a message-producer function
	// sourceID expected to be pump's id
	NewMessageFunc func() *Message
)

// Params support types
type (
	// Identifiable defines a unique component
	Identifiable interface {
		ID() string
		SetID(string)
	}

	// UID is a string unique identifier
	UID struct {
		value string
	}

	// ParamFunc represents a function which applies the param
	ParamFunc func()

	// Param is a structure for delayed parameters apply
	Param struct {
		ID    string
		Apply ParamFunc
	}

	// Params represents current track attributes: time signature, bpm e.t.c.
	Params struct {
		private map[string][]ParamFunc
	}
)

// Small types for common params
type (
	// BufferSize represents a buffer size value
	BufferSize int
	// NumChannels represents a number of channels
	NumChannels int
	// SampleRate represents a sample rate value
	SampleRate int
	// Tempo represents a tempo value
	Tempo float32
	// SamplePosition represents a position in samples measure
	SamplePosition int64
)

// NewParams returns a new params instance with initialised map inside
func NewParams(params ...Param) (result *Params) {
	result = &Params{
		private: make(map[string][]ParamFunc),
	}
	result.Add(params...)
	return
}

// Add accepts a slice of params
func (p *Params) Add(params ...Param) *Params {
	if p == nil {
		p = NewParams(params...)
	} else {
		for _, param := range params {
			private, ok := p.private[param.ID]
			if !ok {
				private = make([]ParamFunc, 0, len(params))
			}
			private = append(private, param.Apply)

			p.private[param.ID] = private
		}
	}

	return p
}

// ApplyTo consumes params defined for consumer in this param set
func (p *Params) ApplyTo(consumer Identifiable) {
	if p == nil {
		return
	}
	if params, ok := p.private[consumer.ID()]; ok {
		for _, param := range params {
			param()
		}
		delete(p.private, consumer.ID())
	}
}

// Merge two param sets into one
func (p *Params) Merge(source *Params) *Params {
	if p == nil || p.Empty() {
		return source
	}
	for newKey, newValues := range source.private {
		if _, ok := p.private[newKey]; ok {
			p.private[newKey] = append(p.private[newKey], newValues...)
		} else {
			p.private[newKey] = newValues
		}
	}
	return p
}

// Empty returns true if params are empty
func (p *Params) Empty() bool {
	if p == nil || p.private == nil || len(p.private) == 0 {
		return true
	}
	return false
}

// ID of the pipe
func (i *UID) ID() string {
	return i.value
}

// SetID to the pipe
func (i *UID) SetID(id string) {
	i.value = id
}

// ReceivedBy returns channel which is closed when param received
func ReceivedBy(wg *sync.WaitGroup, id Identifiable) Param {
	return Param{
		ID: id.ID(),
		Apply: func() {
			fmt.Printf("recieved by %v\n", id.ID())
			wg.Done()
		},
	}
}

// NumChannels returns number of channels in this sample slice
func (b Buffer) NumChannels() NumChannels {
	if b == nil {
		return 0
	}
	return NumChannels(len(b))
}

// Size returns number of samples in single block in this sample slice
func (b Buffer) Size() BufferSize {
	if b.NumChannels() == 0 {
		return 0
	}
	return BufferSize(len((b)[0]))
}

// Append buffers set to existing one one
func (b Buffer) Append(source Buffer) Buffer {
	if b.Size() == 0 {
		return source
	}
	for i := range source {
		b[i] = append(b[i], source[i]...)
	}
	return b
}

// Slice creates a new copy of buffer from start position with defined legth
// if buffer doesn't have enough samples - shorten block is returned
func (b Buffer) Slice(start int64, length int) (result Buffer) {
	if b == nil {
		return
	}
	end := BufferSize(start + int64(length))
	result = Buffer(make([][]float64, b.NumChannels()))
	for i := range b {
		if end > b.Size() {
			end = b.Size()
		}
		result[i] = append(result[i], b[i][start:end]...)
	}
	return
}

// EmptyBuffer returns an empty buffer of specified length
func EmptyBuffer(numChannels NumChannels, bufferSize BufferSize) Buffer {
	result := Buffer(make([][]float64, numChannels))
	for i := range result {
		result[i] = make([]float64, bufferSize)
	}
	return result
}

// Frame creates a new clip from asset with defined start and length
func (b Buffer) Frame(start int64, len int) *Frame {
	return &Frame{
		Buffer: b,
		Start:  start,
		Len:    len,
	}
}
