package gojsonrpc2

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/AnimusPEXUS/gouuidtools"
	"github.com/AnimusPEXUS/utils/worker"
)

type JSONRPC2Node struct {
	// the resulting errors are returned to PushMessageFromOutsied caller.
	//   error #0 - if protocol error
	//   error #1 - on all errors
	OnRequestCB           func(msg *Message) (error, error)
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

	debugName string
}

func NewJSONRPC2Node() *JSONRPC2Node {
	ret := new(JSONRPC2Node)
	ret.debugName = "JSONRPC2Node"
	ret.defaultResponseTimeout = time.Minute
	ret.wrkr = worker.New(ret.workerFunction)
	ret.handlers_mutex = new(sync.Mutex)
	return ret
}

func (self *JSONRPC2Node) SetDebugName(name string) {
	self.debugName = fmt.Sprintf("[%s]", name)
}

func (self *JSONRPC2Node) GetDebugName() string {
	return self.debugName
}

func (self *JSONRPC2Node) DebugPrintln(data ...any) {
	fmt.Println(append(append([]any{}, self.debugName), data...)...)
}

func (self *JSONRPC2Node) DebugPrintfln(format string, data ...any) {
	fmt.Println(append(append([]any{}, self.debugName), fmt.Sprintf(format, data...))...)
}

func (self *JSONRPC2Node) Close() {
	self.stop_flag = true
	self.wrkr.Stop()
}

// send message without performing any protocol compliance checks.
// except: this function resets msg's jsonrpc field
func (self *JSONRPC2Node) SendMessage(msg *Message) error {
	msg.resetJSONRPC2field()
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return self.PushMessageToOutsideCB(b)
}

// returns the final message id.
// this function can be used to automatically generate new Id for message
// and for defining a response handler for response (or errors).
// if request_id_hook is defined, it is to send id used in request being sent
// and waits for signal request_id_hook.Continue before actually perform sending
// use (pass obj created by you) rh param to be used for response handling
func (self *JSONRPC2Node) SendRequest(
	msg *Message,
	genid bool,
	unhandled bool,
	rh *JSONRPC2NodeRespHandler,
	response_timeout time.Duration,
	request_id_hook *JSONRPC2NodeNewRequestIdHook,
) (ret_any any, ret_err error) {

	if debug {
		self.DebugPrintln("SendRequest")
	}

	defer func() {
		xxx := recover()
		if xxx != nil {
			self.DebugPrintln("SendRequest panic:", xxx)
			panic(xxx)
		}

		if debug {
			if ret_err == nil {
				self.DebugPrintln("SendRequest send ok. id:", ret_any)
			}
			self.DebugPrintln("SendRequest defer:", ret_any, ret_err)
		}
	}()

	if debug {
		self.DebugPrintln("SendRequest before Lock()")
	}

	self.handlers_mutex.Lock()
	defer self.handlers_mutex.Unlock()

	if debug {
		self.DebugPrintln("SendRequest after Lock()")
	}

	if !msg.IsRequestAndNotNotification() {
		return nil, errors.New("msg must be request, but not notification")
	}

	var err error

	if self.stop_flag {
		return nil, errors.New("node is closed")
	}

	var id any

	if genid {
		if debug {
			self.DebugPrintln("SendRequest generating id for new message")
		}
		id, err = self.genUniqueId()
		if err != nil {
			return nil, err
		}
		err = msg.SetId(id)
		if err != nil {
			return nil, err
		}
	} else {
		if debug {
			self.DebugPrintln("SendRequest assuming id already in message")
		}
		var ok bool
		id, ok = msg.GetId()
		if !ok && !unhandled {
			// TODO: maybe is's better to panic on invalid value of msg
			// return nil, errors.New("handeled request requires to have id")
			panic("handeled request requires to have id")
		}
	}

	if debug {
		self.DebugPrintln("SendRequest id for new message:", id)
	}

	if request_id_hook != nil {
		if debug {
			self.DebugPrintln("SendRequest sending id back via hook")
		}
		request_id_hook.NewId <- id
		if debug {
			self.DebugPrintln("SendRequest id sent. waiting for Continue signal")
		}
		<-request_id_hook.Continue
		if debug {
			self.DebugPrintln("SendRequest Continue signal received")
		}
	}

	if debug {
		self.DebugPrintln("SendRequest unhandled?:", unhandled)
	}

	if !unhandled {

		if rh == nil {
			return nil, errors.New("handeling response requires rh parameter defined")
		}

		wrapper := new(xJSONRPC2NodeRespHandlerWrapper)

		wrapper.id = id
		wrapper.handler = rh
		wrapper.timeout = response_timeout
		wrapper.orig_timeout = response_timeout

		self.handlers = append(self.handlers, wrapper)

		if debug {
			self.DebugPrintln("SendRequest handler appied and saved")
		}

		go func() {
			if self.wrkr.Status().IsStopped() {
				if debug {
					self.DebugPrintln("SendRequest launching new worker")
				}
				self.wrkr.Start()
			}
		}()
	}

	if debug {
		self.DebugPrintln("SendRequest sending...")
	}

	// note: no goroutine creation here. this is done, so SendRequest
	//       return actual result.
	//
	// in testing environment goroutine must be created by PushMessageToOutsideCB
	// and PushMessageToOutsideCB must return as soon as possible
	// to emulate asynchronous network work
	err = self.SendMessage(msg)
	if err != nil {
		if debug {
			self.DebugPrintln("SendRequest sending error:", err)
		}
		return nil, err
	}

	return id, nil
}

func (self *JSONRPC2Node) SendNotification(msg *Message) error {

	if !msg.IsNotification() {
		return errors.New("not a notification")
	}

	err := self.SendMessage(msg)
	if err != nil {
		return err
	}

	return nil
}

func (self *JSONRPC2Node) SendResponse(msg *Message) error {

	if !msg.IsResponseAndNotError() {
		return errors.New("msg must be response, but not error")
	}

	err := self.SendMessage(msg)
	if err != nil {
		return err
	}

	return nil
}

func (self *JSONRPC2Node) SendError(msg *Message) error {

	if !msg.IsError() {
		return errors.New("SendError() is only for error messages")
	}

	err := self.SendMessage(msg)
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
func (self *JSONRPC2Node) PushMessageFromOutside(data []byte) (error, error) {
	if debug {
		self.DebugPrintln("PushMessageFromOutside() : got message from outside :", string(data))
	}
	if debug {
		defer self.DebugPrintln("PushMessageFromOutside() : exit")
	}

	var msg = new(Message)
	err := json.Unmarshal(data, &msg)
	if err != nil {
		return err, errors.New("protocol error")
	}

	err = msg.checkJSONRPC2field()
	if err != nil {
		return err, errors.New("protocol error")
	}

	err = msg.IsInvalidError()
	if err != nil {
		return errors.New("invalid message structure: " + err.Error()),
			errors.New("protocol error")
	}

	if debug {
		self.DebugPrintln("is request or response?")
	}
	if msg.HasRequestFields() {
		if debug {
			self.DebugPrintln(" - request")
		}
		return self.OnRequestCB(msg)
	} else {
		if debug {
			self.DebugPrintln(" - response")
		}

		id, ok := msg.GetId()
		if !ok {
			return errors.New("no id field for response message"),
				errors.New("protocol error")
		}

		func() {
			if debug {
				defer self.DebugPrintln("PushMessageFromOutside() : before Lock() 1")
			}
			self.handlers_mutex.Lock()
			defer self.handlers_mutex.Unlock()
			if debug {
				defer self.DebugPrintln("PushMessageFromOutside() : after Lock() 1")
			}

			found := false

			for xi, x := range self.handlers {
				if debug {
					self.DebugPrintln(fmt.Sprintf("searching for handler: %s ? %s", id, x.id))
				}
				if id == x.id {
					found = true
					go x.handler.OnResponse(msg)
					self.handlers = append(self.handlers[:xi], self.handlers[xi+1:]...)
					break
				}
			}

			if !found {
				if self.OnUnhandledResponseCB != nil {
					go self.OnUnhandledResponseCB(msg)
				}
			}
		}()

	}

	return nil, nil
}

func (self *JSONRPC2Node) genUniqueId(
// lrc *golockerreentrancycontext.LockerReentrancyContext,
) (string, error) {
	var ret string

main_loop:
	for true {
		u, err := gouuidtools.NewUUIDFromRandom()
		if err != nil {
			return "", err
		}

		ret = u.Format()
		for _, x := range self.handlers {
			x_id_str, ok := (x.id).(string)
			if ok {
				if x_id_str == ret {
					continue main_loop
				}
			}
		}
		break
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

type JSONRPC2NodeNewRequestIdHook struct {
	NewId    chan<- any
	Continue <-chan struct{}
}

func NewChannelledJSONRPC2NodeRespHandler() (
	timedout <-chan struct{},
	closed <-chan struct{},
	msg <-chan *Message,
	rh *JSONRPC2NodeRespHandler,
) {
	var (
		ret_timedout chan struct{}
		ret_closed   chan struct{}
		ret_msg      chan *Message
	)

	ret := &JSONRPC2NodeRespHandler{
		OnTimeout: func() {
			ret_timedout <- struct{}{}
		},
		OnClose: func() {
			ret_closed <- struct{}{}
		},
		OnResponse: func(message *Message) {
			ret_msg <- message
		},
	}
	return ret_timedout, ret_closed, ret_msg, ret
}
