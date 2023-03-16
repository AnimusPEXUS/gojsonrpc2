package gojsonrpc2

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

type JSONRPC2MultiplexerBufferWrapper struct {
	BufferId  string
	RequestId any
	Buffer    io.ReadSeeker
	Mutex     sync.Mutex
	debugName string
}

func (self *JSONRPC2MultiplexerBufferWrapper) SetDebugName(name string) {
	self.debugName = name
}

func (self *JSONRPC2MultiplexerBufferWrapper) DebugPrintln(data ...any) {
	fmt.Println(append(append([]any{}, self.debugName), data...)...)
}

func (self *JSONRPC2MultiplexerBufferWrapper) BufferSize() (int64, error) {
	self.Mutex.Lock()
	defer self.Mutex.Unlock()
	return self.intBufferSize()
}

func (self *JSONRPC2MultiplexerBufferWrapper) intBufferSize() (int64, error) {
	return self.Buffer.Seek(0, io.SeekEnd)
}

func (self *JSONRPC2MultiplexerBufferWrapper) BufferSlice(start int64, end int64) (ret_bytes []byte, ret_err error) {
	self.Mutex.Lock()
	defer self.Mutex.Unlock()
	return self.intBufferSlice(start, end)
}

func (self *JSONRPC2MultiplexerBufferWrapper) intBufferSlice(start int64, end int64) (ret_bytes []byte, ret_err error) {

	if debug {
		self.DebugPrintln("BufferSlice", start, end)
	}

	defer func() {
		if debug {
			self.DebugPrintln("BufferSlice defer", ret_bytes, ret_err)
		}
	}()

	if start < 0 {
		return nil, errors.New("invalid 'start' value")
	}

	if end < 0 {
		return nil, errors.New("invalid 'end' value")
	}

	if debug {
		self.DebugPrintln("self.intBufferSize", start)
	}
	size, err := self.intBufferSize()
	if err != nil {
		return nil, err
	}

	if end > size {
		return nil, errors.New("'end' exceeds buffer size")
	}

	if debug {
		self.DebugPrintln("self.Buffer.Seek", start)
	}
	_, err = self.Buffer.Seek(start, io.SeekStart)
	if err != nil {
		return nil, err
	}

	x := make([]byte, end-start)

	if debug {
		self.DebugPrintln("io.ReadFull(self.Buffer, ", x, ")")
	}
	_, err = io.ReadFull(self.Buffer, x)
	if err != nil {
		return nil, err
	}

	return x, nil
}
