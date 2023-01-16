package gojsonrpc2

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	uuid "github.com/satori/go.uuid"

	"github.com/AnimusPEXUS/utils/worker"
)

type JSONRPC2Node struct {
	OnRequestCB           func(msg *Message)
	OnUnhandledResponseCB func(msg *Message)

	// JSONRPC2Node doesn't use error returned by this CB. error is simply
	// passed to SendMessage caller
	PushMessageToOutsideCB func(data []byte) error

	// default timeout for response waiting. set it to 0 to disable timeout
	// TODO: look's like it's not used in code
	defaultResponseTimeout time.Duration

	handlers_mutex *sync.Mutex
	handlers       []*xJSONRPC2NodeRespHandlerWrapper

	wrkr *worker.Worker

	stop_flag bool // also this closes node
}

func NewJSONRPC2Node() *JSONRPC2Node {
	ret := new(JSONRPC2Node)
	ret.defaultResponseTimeout = time.Minute
	ret.wrkr = worker.New(ret.workerFunction)
	ret.handlers_mutex = new(sync.Mutex)
	return ret
}

func (self *JSONRPC2Node) Close() {
	self.stop_flag = true
	self.wrkr.Stop()
}

func (self *JSONRPC2Node) SendMessage(msg *Message) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return self.PushMessageToOutsideCB(b)
}

// returns the final message id.
// this function can be used to automatically generate new Id for message
// and for defining a response handler for response (or errors)
func (self *JSONRPC2Node) SendRequest(
	msg *Message,
	genid bool,
	unhandled bool,
	rh *JSONRPC2NodeRespHandler,
	response_timeout time.Duration,
) (any, error) {

	self.handlers_mutex.Lock()
	defer self.handlers_mutex.Unlock()

	msg.Error = nil
	msg.Result = nil

	var err error

	if self.stop_flag {
		return nil, errors.New("node is closed")
	}

	var id any

	if genid {
		id, err = self.genUniqueId()
		if err != nil {
			return nil, err
		}
		err = msg.SetId(id)
		if err != nil {
			return nil, err
		}
	} else {
		var ok bool
		id, ok = msg.GetId()
		if !ok && !unhandled {
			// TODO: maybe is's better to panic on invalid malue of msg
			// return nil, errors.New("handeled request requires to have id")
			panic("handeled request requires to have id")
		}
	}

	if !unhandled {

		wrapper := new(xJSONRPC2NodeRespHandlerWrapper)

		wrapper.id = id
		wrapper.timeout = response_timeout
		wrapper.orig_timeout = response_timeout

		self.handlers = append(self.handlers, wrapper)

		go func() {
			if self.wrkr.Status().IsStopped() {
				self.wrkr.Start()
			}
		}()
	}

	err = self.SendMessage(msg)
	if err != nil {
		return nil, err
	}

	return id, nil
}

func (self *JSONRPC2Node) SendResponse(msg *Message) error {

	var err error

	msg.Method = ""
	msg.Params = nil
	msg.Error = nil

	if !msg.HaveId() {
		panic("response messages requires id field to be set")
	}

	if msg.Result == nil {
		panic("resulting messages requires Result field to be set")
	}

	err = self.SendMessage(msg)
	if err != nil {
		return err
	}

	return nil
}

func (self *JSONRPC2Node) SendError(msg *Message) error {

	var err error

	msg.Method = ""
	msg.Params = nil
	msg.Result = nil

	if !msg.IsError() {
		return errors.New("not an error message")
	}

	err = self.SendMessage(msg)
	if err != nil {
		return err
	}

	return nil
}

func (self *JSONRPC2Node) workerFunction(
	set_starting func(),
	set_working func(),
	set_stopping func(),
	set_stopped func(),

	is_stop_flag func() bool) {

	set_starting()
	defer func() {
		set_stopped()
	}()

	set_working()

	var make_break bool

	for true {
		func() {
			self.handlers_mutex.Lock()
			defer self.handlers_mutex.Unlock()

			if is_stop_flag() {
				make_break = true
				return
			}

			for i := len(self.handlers) - 1; i != -1; i += -1 {
				h := self.handlers[i]
				v := h.timeout

				if v < time.Second {
					if v == 0 {
						self.handlers = append(self.handlers[0:i], self.handlers[i+1:]...)
						go h.handler.OnTimeout()
					} else {
						h.timeout = 0
					}
				} else {
					h.timeout = v - time.Second
				}
			}
		}()
		if make_break {
			break
		}

		time.Sleep(time.Second)
	}
	set_stopping()

	self.handlers_mutex.Lock()
	defer self.handlers_mutex.Unlock()

	for _, i := range self.handlers {
		go i.handler.OnClose()
	}

	self.handlers = self.handlers[0:0]
}

// returns true on successful operation
func (self *JSONRPC2Node) ResetResponseTimeout(
	id any,
	new_orig_timeout time.Duration,
) bool {
	self.handlers_mutex.Lock()
	defer self.handlers_mutex.Unlock()

	for _, x := range self.handlers {
		if x.id == id {
			if new_orig_timeout != 0 {
				x.orig_timeout = new_orig_timeout
			}
			x.id = x.orig_timeout
			return true
		}
	}
	return false
}

// push new message from outside into node
// returned values:
//
//	#0 protocol violation - not critical for server running,
//	#1 other errors - should be treated as server errors
func (self *JSONRPC2Node) PushMessageFromOutsied(data []byte) (error, error) {
	var msg = new(Message)
	err := json.Unmarshal(data, &msg)
	if err != nil {
		return err, nil
	}

	err = msg.checkJSONRPC2field()
	if err != nil {
		return err, nil
	}

	err = msg.IsInvalid()
	if err != nil {
		return errors.New("invalid message structure: " + err.Error()), nil
	}

	if msg.HasRequestFields() {
		go self.OnRequestCB(msg)
	} else {
		id, ok := msg.GetId()
		if !ok {
			return errors.New("no id field for response message"), nil
		}

		func() {
			self.handlers_mutex.Lock()
			defer self.handlers_mutex.Unlock()

			found := false

			for xi, x := range self.handlers {
				if id == x.id {
					found = true
					go x.handler.OnResponse(msg)
					self.handlers = append(self.handlers[:xi], self.handlers[xi+1:]...)
					break
				}
			}

			if !found {
				go self.OnUnhandledResponseCB(msg)
			}
		}()

	}

	return nil, nil
}

func (self *JSONRPC2Node) genUniqueId() (string, error) {
	var ret string

	for true {
		u, err := uuid.NewV4()
		if err != nil {
			return "", err
		}
		ret = strings.ToLower(u.String())
		for _, x := range self.handlers {
			x_id_str, ok := (x.id).(string)
			if ok {
				if x_id_str == ret {
					continue
				}
			}
		}

	}

	return ret, nil
}

type xJSONRPC2NodeRespHandlerWrapper struct {
	handler      *JSONRPC2NodeRespHandler
	id           any
	timeout      time.Duration
	orig_timeout time.Duration
}

type JSONRPC2NodeRespHandler struct {
	OnTimeout  func()
	OnClose    func()
	OnResponse func(message *Message)
}
