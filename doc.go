/*
Package pipe allows to build and execute DSP pipelines.

Concept

This package offers an opinionated perspective to DSP. It's based on the
idea that the signal processing can have up to three stages:

    Source - the origin of signal;
    Processor - the manipulator of the signal;
    Sink - the destination of signal;

It implies the following constraints:

    Source and Sink are mandatory;
    There might be 0 to n Processors;
    All stages are executed sequentially.

The implementation is based on the pipeline pattern explained in the go
blog https://blog.golang.org/pipelines. Which means that stages are also
executed asynchronously.

Components

Each stage in the pipeline is implemented by components. For example,
wav.Source reads signal from wav file and vst2.Processor processes signal
with vst2 plugin. Components are instantiated with allocator functions:

    SourceAllocatorFunc
    ProcessorAllocatorFunc
    SinkAllocatorFunc

Allocator functions return component structures and pre-allocate all
required resources and structures. It reduces number of allocations during
pipeline execution and improves latency.

Component structures consist of mutability, run closure and flush hook. Run
closure is the function which will be called during the pipeline run. Flush
hook is triggered when pipe is done or interruped by error or timeout. It
enables to execute proper clean up logic. For mutability, refer to
mutability package documentation.

Routing and binding

To run the pipeline, one first need to build it. It starts with a routing:

    route := pipe.Routing{
        Source: &wav.Source{Reader: reader},
        Processors: pipe.Processors(
            vst2.Open(vstPath1),
            vst2.Open(vstPath2),
        ),
        Sink: &wav.Sink{Writer: writer},
    }

Routing defines the order in which DSP components form the pipeline. Once
routing is defined, components can be bound together. It's done by creating
a line:

    line, err := route.Line(bufferSize)

Line executes all allocators provided in routing and binds components
together.

Execution

Once components are routed and bound to line, they can be executed. To do
that pipe should be created:

    p := pipe.New(
        context.Background(),
        pipe.WithLines(line),
	)
	err := p.Wait()

Pipe will asynchronously run all DSP components until either source or
context is done.
*/
package pipe
