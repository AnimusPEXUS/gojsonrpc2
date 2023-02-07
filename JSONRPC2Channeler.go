package gojsonrpc2

// JSONRPC2Channeler is a protocol for quantisizing (and channelizing)
// data being sent through single data stream.
//
// ChannelData() can be called asyncronously with data which need to be channeled.
//
// JSONRPC2Channeler tells the other side about new buffer on this side's JSONRPC2Channeler.
// the other side's JSONRPC2Channeler then sequentially asks for bits of buffer, until other
// side gets the whole buffer. after other side's JSONRPC2Channeler get's complete buffer,
// it may pass (using it's OnDataCB callback) it for farther consumption.
//
// JSONRPC2Channeler calls PushMessageToOutsideCB when it's need to send data to other side

import (
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	uuid "github.com/satori/go.uuid"

	"github.com/AnimusPEXUS/golockerreentrancycontext"
)

const (
	JSONRPC2_CHANNELER_METHOD_NEW_BUFFER_AVAILABLE = "n"
	JSONRPC2_CHANNELER_METHOD_GET_BUFFER_INFO      = "gbi"
	JSONRPC2_CHANNELER_METHOD_GET_BUFFER_SLICE     = "gbs"
)

const debug = true
const verbose = true

type JSONRPC2Channeler struct {
	PushMessageToOutsideCB func(data []byte) error

	OnDataCB func(data []byte)
	// OnPeerProtocolErrorCB func(error)

	buffer_wrappers        []*BufferWrapper
	buffer_wrappers_mutex2 *sync.Mutex

	jrpc_node *JSONRPC2Node
}

func NewJSONRPC2Channeler() *JSONRPC2Channeler {
	self := new(JSONRPC2Channeler)

	self.buffer_wrappers_mutex2 = new(sync.Mutex)

	self.jrpc_node = NewJSONRPC2Node()
	self.jrpc_node.OnRequestCB = func(m *Message) (error, error) {
		_, _, proto_err, err := self.handle_jrpcOnRequestCB(m, nil)
		if proto_err != nil || err != nil {
			return proto_err, err
		}
		return nil, nil
	}
	self.jrpc_node.PushMessageToOutsideCB = self.jrpcPushMessageToOutsideCB

	return self
}

func (self *JSONRPC2Channeler) Verbose(data ...any) {
	fmt.Println(data...)
}

func (self *JSONRPC2Channeler) Debug(data ...any) {
	fmt.Println(append(append([]any{}, "[d]"), data...)...)
}

func (self *JSONRPC2Channeler) Close() {
	self.jrpc_node.Close()
	self.jrpc_node = nil
	self.buffer_wrappers = nil
}

func (self *JSONRPC2Channeler) requestSendingRespWaitingRoutine(
	msg *Message,
	request_id_hook *JSONRPC2NodeNewRequestIdHook,
) (
	timeout bool,
	closed bool,
	resp *Message,

	// TODO: protocol_err reserved for possible protocol errors (and more flefible error handling)
	protocol_err error,

	err error,
) {

	retry_countdown := 3
retry_label:

	var (
		chan_timeout  = make(chan struct{})
		chan_close    = make(chan struct{})
		chan_response = make(chan *Message)
	)

	_, err = self.jrpc_node.SendRequest(
		msg,
		true,
		false,
		&JSONRPC2NodeRespHandler{
			OnTimeout: func() {
				chan_timeout <- struct{}{}
			},
			OnClose: func() {
				chan_close <- struct{}{}
			},
			OnResponse: func(resp2 *Message) {
				chan_response <- resp2
			},
		},
		// mayde it's better to make this definable using parameter, but minute
		// is timeout specified by protocol of Channeler
		time.Minute,
		request_id_hook,
	)
	if err != nil {
		return false, false, nil, nil, err
	}

	select {
	case <-chan_timeout:
		fmt.Println("timeout waiting for buffer info from peer")
		if retry_countdown != 0 {
			retry_countdown--
			fmt.Println("   retrying to get buffer info")
			goto retry_label
		}
		return true, false, nil, nil, errors.New("timeout")
	case <-chan_close:
		fmt.Println("waited for message from peer, but local node is closed")
		return false, true, nil, nil, errors.New("node closed")
	case resp = <-chan_response:

		proto_err := resp.IsInvalidError()
		if proto_err != nil {
			return false, false, nil, proto_err, errors.New("protocol error")
		}

		return false, false, resp, nil, nil
	}
}

func (self *JSONRPC2Channeler) jrpcOnRequestCB_NEW_BUFFER_AVAILABLE(
	msg *Message,
) (
	timedout bool,
	closed bool,
	proto_err error,
	err error,
) {
	var msg_par map[string]string

	msg_par, ok := (msg.Params).(map[string]string)
	if !ok {
		return false,
			false,
			errors.New("can't convert msg.Params to map[string]string"),
			errors.New("protocol error")
	}

	buffid, ok := msg_par["buffid"]
	if !ok {
		return false,
			false,
			errors.New("buffid parameter not found"),
			errors.New("protocol error")
	}

	var buffer_info_resp *JSONRPC2Channeler_proto_BufferInfo

	{
		timedout, closed, buffer_info_resp, proto_err, err =
			self.getBuffInfo(buffid, time.Minute)

		if proto_err != nil || err != nil {
			return timedout, closed, proto_err, err
		}
	}

	// TODO: add error checks?

	// TODO: add size limit?

	buf_size := buffer_info_resp.Size

	result_buff := make([]byte, buf_size)

	slice_size := 512

	buf_size_div_slice_size := buf_size / slice_size

	iterations_count := int(buf_size_div_slice_size)
	last_size := buf_size_div_slice_size - (slice_size * iterations_count)

	for i := 0; i != iterations_count; i++ {

		buff_start := slice_size * i
		buff_end := slice_size * (i + 1)

		retry_countdown := 3

	retry_label2:
		timedout, closed, proto_err, err := self.getBuffSlice(
			result_buff,
			buffid,
			buff_start,
			buff_end,
			time.Minute,
		)

		if !timedout && !closed {
			if proto_err != nil || err != nil {
				return false, false, proto_err, err
			}
		}

		if retry_countdown != 0 {
			retry_countdown--
			goto retry_label2
		}

		return timedout, closed, proto_err, err
	}

	if last_size > 0 {
		buff_start := slice_size * iterations_count
		buff_end := buff_start + last_size
		retry_countdown := 3

	retry_label3:
		timedout, closed, proto_err, err := self.getBuffSlice(
			result_buff,
			buffid,
			buff_start,
			buff_end,
			time.Minute,
		)

		if !timedout && !closed {
			if proto_err != nil || err != nil {
				return false, false, proto_err, err
			}
		}

		if retry_countdown != 0 {
			retry_countdown--
			goto retry_label3
		}

		return timedout, closed, proto_err, err
	}

	go self.OnDataCB(result_buff)

	return false, false, nil, nil
}

// no re-entrant locks in golang
func (self *JSONRPC2Channeler) getBuffByIdLocal(
	id string,
	lrc *golockerreentrancycontext.LockerReentrancyContext,
) (
	bw *BufferWrapper,
	ok bool,
) {
	if lrc == nil {
		lrc = new(golockerreentrancycontext.LockerReentrancyContext)
	}

	if debug {
		self.Debug("before Lock()")
	}

	lrc.LockMutex(self.buffer_wrappers_mutex2)
	defer lrc.UnlockMutex(self.buffer_wrappers_mutex2)

	if debug {
		self.Debug("after Lock()")
	}

	for _, x := range self.buffer_wrappers {
		if x.BufferId == id {
			return x, true
		}
	}
	return nil, false
}

func (self *JSONRPC2Channeler) jrpcOnRequestCB_GET_BUFFER_INFO(
	msg *Message,
	lrc *golockerreentrancycontext.LockerReentrancyContext,
) (
	timedout bool,
	closed bool,
	proto_error error,
	err error,
) {
	if lrc == nil {
		lrc = new(golockerreentrancycontext.LockerReentrancyContext)
	}

	var msg_par map[string]string

	msg_par, ok := (msg.Params).(map[string]string)
	if !ok {
		return false,
			false,
			errors.New("can't convert msg.Params to map[string]string"),
			errors.New("protocol error")
	}

	buffid, ok := msg_par["buffid"]
	if !ok {
		return false,
			false,
			errors.New("buffid parameter not found"),
			errors.New("protocol error")
	}

	bw, ok := self.getBuffByIdLocal(buffid, lrc)
	if !ok {
		// fmt.Println("jrpcOnRequestCB: client tried to request unexisting buffer")
		return false,
			false,
			nil,
			errors.New("invalid buffer id")
	}

	info := new(JSONRPC2Channeler_proto_BufferInfo)
	info.Size = len(bw.Buffer)

	// TODO: next not checked. thinking and checking required

	resp := new(Message)
	resp.Response.Result = info
	resp.Error = nil

	err = self.jrpc_node.SendResponse(resp)
	if err != nil {
		return false, false, nil, err
	}

	return false, false, nil, nil
}

func (self *JSONRPC2Channeler) handle_jrpcOnRequestCB(
	msg *Message,
	lrc *golockerreentrancycontext.LockerReentrancyContext,
) (
	timeout bool,
	closed bool,
	proto_error error,
	all_error error,
) {
	if lrc == nil {
		lrc = new(golockerreentrancycontext.LockerReentrancyContext)
	}

	if !msg.IsRequestAndNotNotification() {
		// TODO: report errors
	}

	resp := new(Message)

	defer func() {
		// TODO: add error handling?
		self.jrpc_node.SendMessage(resp)
	}()

	resp.Error = &JSONRPC2Error{
		Code:    -32603,
		Message: "Internal error",
	}

	resp.Id = msg.Id
	switch msg.Method {
	default:
		proto_error = errors.New("peer requested unsupported Method")
		all_error = errors.New("protocol error")
		resp.Error.Code = -32000
		resp.Error.Message = "protocol error"
		return

	case JSONRPC2_CHANNELER_METHOD_NEW_BUFFER_AVAILABLE:
		timeout, closed, proto_error, all_error =
			self.jrpcOnRequestCB_NEW_BUFFER_AVAILABLE(msg)
		if proto_error != nil {
			resp.Error.Code = -32000
			resp.Error.Message = "protocol error"
		}
		return

	case JSONRPC2_CHANNELER_METHOD_GET_BUFFER_INFO:
		// TODO: reset timeout for JSONRPC2_CHANNELER_METHOD_NEW_BUFFER_AVAILABLE request
		timeout, closed, proto_error, all_error =
			self.jrpcOnRequestCB_GET_BUFFER_INFO(msg, lrc)
		if proto_error != nil {
			resp.Error.Code = -32000
			resp.Error.Message = "protocol error"
		}
		return
	}

}

// results:
// #0 bool - timedout
// #1 bool - closed
// #2 *JSONRPC2Channeler_proto_BufferInfo
// #2 invalid response (protocol) error - not nil in case if it's protocol error
// #3 error
func (self *JSONRPC2Channeler) getBuffInfo(
	buffid string,
	timeout time.Duration,
) (bool, bool, *JSONRPC2Channeler_proto_BufferInfo, error, error) {
	m := new(Message)
	m.Method = JSONRPC2_CHANNELER_METHOD_GET_BUFFER_INFO
	m.Params = map[string]string{"buffid": buffid}
	timedout, closed, resp, proto_eror, err :=
		self.requestSendingRespWaitingRoutine(m, nil)
	if proto_eror != nil || err != nil {
		return timedout, closed, nil, proto_eror, err
	}

	resp_map, ok := resp.Result.(map[string]any)
	if !ok {
		return false,
			false,
			nil,
			errors.New("couldn't use BuffInfo response as object"),
			nil
	}

	// TODO: checks required

	ret := new(JSONRPC2Channeler_proto_BufferInfo)
	ret.Size, ok = resp_map["s"].(int)
	if !ok {
		return false,
			false,
			nil,
			errors.New("couldn't get buff size value as integer from response"),
			nil
	}

	return false, false, ret, nil, nil
}

// results:
// #0 bool - timeout
// #1 bool - closed
// #2 invalid response (protocol) error - not nil in case if it's protocol error
// #3 error
func (self *JSONRPC2Channeler) getBuffSlice(
	target_buff []byte,
	buffid string,
	buff_start int,
	buff_end int,
	timeout time.Duration,
) (bool, bool, error, error) {

	if buff_start < 0 {
		return false, false, nil, errors.New("invalid values for buff_start")
	}

	if buff_start > buff_end {
		return false, false, nil, errors.New("invalid values for buff_start/buff_end")
	}

	control_size := buff_end - buff_start

	var resp_msg *Message
	{
		m := new(Message)
		m.Method = JSONRPC2_CHANNELER_METHOD_GET_BUFFER_SLICE
		m.Params = map[string]any{
			"buffid": buffid,
			"start":  buff_start,
			"end":    buff_end,
		}

		var (
			timedout   bool
			closed     bool
			proto_eror error
			err        error
		)

		timedout, closed, resp_msg, proto_eror, err =
			self.requestSendingRespWaitingRoutine(m, nil)

		if proto_eror != nil || err != nil {
			return timedout, closed, proto_eror, err
		}
	}

	if !resp_msg.IsError() {
		val := resp_msg.Result.(string)
		val_b, err := base64.StdEncoding.DecodeString(val)
		if err != nil {
			return false, false, nil, err
		}

		len_b := len(val_b)

		if len_b != control_size {
			return false, false,
				errors.New("peer returned buffer with invalid size"),
				errors.New("protocol error")
		}

		copy(target_buff[buff_start:], val_b)

	}

	return false, false, nil, nil
}

func (self *JSONRPC2Channeler) jrpcPushMessageToOutsideCB(
	data []byte,
) error {
	self.PushMessageToOutsideCB(data)
	// TODO: handle errors?
	return nil
}

// use this function to send the data. this function will not return until
// send succeed or fail
func (self *JSONRPC2Channeler) ChannelData(data []byte) (
	timedout bool,
	closed bool,
	resp_msg *Message,
	proto_err error,
	err error,
) {

	lrc := new(golockerreentrancycontext.LockerReentrancyContext)

	if verbose {
		self.Verbose("[i]", "got data to channel:", data)
	}

	var buffer_id string
	var request_id any

	wrapper := new(BufferWrapper)

	func() {
		lrc.LockMutex(self.buffer_wrappers_mutex2)
		defer lrc.UnlockMutex(self.buffer_wrappers_mutex2)

		if debug {
			self.Debug("generating id for new buffer")
		}

		buffer_id, err = self.genUniqueBufferId(lrc)
		if err != nil {
			return
		}

		if verbose {
			self.Verbose("[i]", "new id for buffer:", buffer_id)
		}

		wrapper.BufferId = buffer_id
		wrapper.Buffer = data

		self.buffer_wrappers = append(self.buffer_wrappers, wrapper)
	}()
	if err != nil {
		return false, false, nil, nil, err
	}

	defer func() {
		lrc.LockMutex(self.buffer_wrappers_mutex2)
		defer lrc.UnlockMutex(self.buffer_wrappers_mutex2)

		for i := len(self.buffer_wrappers) - 1; i != -1; i += -1 {
			if self.buffer_wrappers[i].BufferId == buffer_id {
				self.buffer_wrappers = append(
					self.buffer_wrappers[:i],
					self.buffer_wrappers[i+1:]...,
				)
			}
		}
	}()

	new_buffer_msg := new(JSONRPC2Channeler_proto_NewBufferMsg)
	new_buffer_msg.BufferId = buffer_id

	channel_start_msg := new(Message)
	channel_start_msg.Method = JSONRPC2_CHANNELER_METHOD_NEW_BUFFER_AVAILABLE
	channel_start_msg.Params = new_buffer_msg

	var (
		new_id_chan   chan any
		continue_chan chan struct{}
	)

	new_id_chan = make(chan any)
	continue_chan = make(chan struct{})

	hook := new(JSONRPC2NodeNewRequestIdHook)
	hook.NewId = new_id_chan
	hook.Continue = continue_chan

	go func() {
		request_id = <-new_id_chan
		lrc.LockMutex(self.buffer_wrappers_mutex2)
		defer lrc.UnlockMutex(self.buffer_wrappers_mutex2)
		wrapper.RequestId = request_id
		continue_chan <- struct{}{}
	}()

	timedout, closed, resp_msg, proto_err, err =
		self.requestSendingRespWaitingRoutine(
			channel_start_msg,
			hook,
		)
	if proto_err != nil || err != nil {
		return false, false, nil, proto_err, err
	}

	return false, false, resp_msg, nil, nil
}

// this have protocol restriction on input data size
// #0 - protocol error
// #1 - all errors
func (self *JSONRPC2Channeler) PushMessageFromOutside(data []byte) (error, error) {
	if len(data) >= 1050 {
		return errors.New("data is too big. must be < 1050"),
			errors.New("protocol error")
	}
	return self.jrpc_node.PushMessageFromOutside(data)
}

// TODO: ?
// func (self *JSONRPC2Channeler) PushMessage(data []byte) (error, error) {
// 	return nil, nil
// }

func (self *JSONRPC2Channeler) genUniqueBufferId(
	lrc *golockerreentrancycontext.LockerReentrancyContext,
) (string, error) {
	if lrc == nil {
		lrc = new(golockerreentrancycontext.LockerReentrancyContext)
	}

	var ret string

	for true {
		if debug {
			self.Debug("generating new UUID")
		}
		u := uuid.NewV4()

		ret := u.String()

		if debug {
			self.Debug(ret)
		}

		if debug {
			self.Debug("testing")
		}
		_, ok := (self.getBuffByIdLocal(ret, lrc))
		if !ok {
			if debug {
				self.Debug("doesn't exists already - ok")
			}
			break
		}
		if debug {
			self.Debug("retry")
		}
	}

	return ret, nil
}

type BufferWrapper struct {
	BufferId  string
	RequestId any
	Buffer    []byte
}
