package gojsonrpc2

import (
	"errors"
	"io"
	"sync"
)

type JSONRPC2ChannelerBufferWrapper struct {
	BufferId  string
	RequestId any
	Buffer    io.ReadSeeker
	Mutex     sync.Mutex
}

func (self *JSONRPC2ChannelerBufferWrapper) BufferSize() (int64, error) {
	self.Mutex.Lock()
	defer self.Mutex.Unlock()

	return self.Buffer.Seek(0, io.SeekEnd)
}

func (self *JSONRPC2ChannelerBufferWrapper) BufferSlice(start int64, end int64) ([]byte, error) {

	self.Mutex.Lock()
	defer self.Mutex.Unlock()

	if start < 0 {
		return nil, errors.New("invalid 'start' value")
	}

	if end < 0 {
		return nil, errors.New("invalid 'end' value")
	}

	size, err := self.BufferSize()
	if err != nil {
		return nil, err
	}

	if end > size {
		return nil, errors.New("'end' exceeds buffer size")
	}

	_, err = self.Buffer.Seek(start, io.SeekStart)
	if err != nil {
		return nil, err
	}

	x := make([]byte, end-start)

	_, err = io.ReadFull(self.Buffer, x)
	if err != nil {
		return nil, err
	}

	return x, nil
}
