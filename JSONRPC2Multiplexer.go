package gojsonrpc2

// JSONRPC2Multiplexer is a protocol for quantisizing (and channelizing)
// data being sent through single data stream.
//
// ChannelData() can be called asyncronously with data which need to be channeled.
//
// JSONRPC2Multiplexer tells the other side about new buffer on this side's JSONRPC2Multiplexer.
// the other side's JSONRPC2Multiplexer then sequentially asks for bits of buffer, until other
// side gets the whole buffer. after other side's JSONRPC2Multiplexer get's complete buffer,
// it may pass (using it's OnDataCB callback) it for farther consumption.
//
// JSONRPC2Multiplexer calls PushMessageToOutsideCB when it's need to send data to other side

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"

	uuid "github.com/satori/go.uuid"

	"github.com/AnimusPEXUS/goinmemfile"
	"github.com/AnimusPEXUS/golockerreentrancycontext"
)

const (
	JSONRPC2_MULTIPLEXER_METHOD_NEW_BUFFER_AVAILABLE = "n"
	JSONRPC2_MULTIPLEXER_METHOD_GET_BUFFER_INFO      = "gbi"
	JSONRPC2_MULTIPLEXER_METHOD_GET_BUFFER_SLICE     = "gbs"
)

type JSONRPC2Multiplexer struct {
	PushMessageToOutsideCB func(data []byte) error

	// you must allocate buffer of size 'size' and pass it as WriteSeeker
	// by calling provide_data_destination
	OnRequestToProvideWriteSeekerCB func(
		size int64,
		provide_data_destination func(io.WriteSeeker) error,
	) error
	// indicates what data transfer to writeseeker passed using
	// OnRequestToProvideWriteSeekerCB is complete
	OnIncommingDataTransferComplete func(io.WriteSeeker)

	buffer_wrappers        []*JSONRPC2MultiplexerBufferWrapper
	buffer_wrappers_mutex2 *sync.Mutex

	jrpc_node *JSONRPC2Node

	debugName string
}

func NewJSONRPC2Multiplexer() *JSONRPC2Multiplexer {
	self := new(JSONRPC2Multiplexer)
	self.debugName = "JSONRPC2Multiplexer"

	self.buffer_wrappers_mutex2 = new(sync.Mutex)

	self.jrpc_node = NewJSONRPC2Node()
	self.jrpc_node.OnRequestCB = func(m *Message) (error, error) {
		if debug {
			self.DebugPrintln("got request from jsonrpc node")
			self.DebugPrintln("passing to self.handle_jrpcOnRequestCB")
		}
		_, _, proto_err, err := self.handle_jrpcOnRequestCB(m, nil)
		if proto_err != nil || err != nil {
			return proto_err, err
		}
		return nil, nil
	}
	self.jrpc_node.PushMessageToOutsideCB = self.jrpcPushMessageToOutsideCB

	return self
}

func (self *JSONRPC2Multiplexer) SetDebugName(name string) {
	self.debugName = fmt.Sprintf("[%s]", name)
	self.jrpc_node.SetDebugName(fmt.Sprintf("%s [JSONRPC2Node]", self.debugName))
}

func (self *JSONRPC2Multiplexer) DebugPrintln(data ...any) {
	fmt.Println(append(append([]any{}, self.debugName), data...)...)
}

func (self *JSONRPC2Multiplexer) DebugPrintfln(format string, data ...any) {
	fmt.Println(append(append([]any{}, self.debugName), fmt.Sprintf(format, data...))...)
}

func (self *JSONRPC2Multiplexer) Close() {
	self.jrpc_node.Close()
	self.jrpc_node = nil
	self.buffer_wrappers = nil
}

func (self *JSONRPC2Multiplexer) DefaultOnRequestToProvideWriteSeekerCB(
	size int64,
	provide_data_destination func(io.WriteSeeker) error,
) error {
	//b := bytes.NewBuffer(make([]byte, size))
	imf := goinmemfile.NewInMemFileFromBytes(make([]byte, size), 0, false)
	// bufio.NewWriterSize()
	// bufio.NewReadWriter
	err := provide_data_destination(imf)
	if err != nil {
		return err
	}
	return nil
}

func (self *JSONRPC2Multiplexer) requestSendingRespWaitingRoutine(
	msg *Message,
	request_id_hook *JSONRPC2NodeNewRequestIdHook,
) (
	timeout bool,
	closed bool,
	resp *Message,
	proto_err error,
	err error,
) {

	if debug {
		self.DebugPrintln(
			"requestSendingRespWaitingRoutine.",
			"msg:", msg.Method, msg.Params,
		)
	}

	defer func() {
		if debug {
			self.DebugPrintln(
				"requestSendingRespWaitingRoutine. defer results:",
				timeout, closed, resp, proto_err, err,
			)
		}
	}()

	retry_countdown := 3
retry_label:

	var (
		chan_timeout  = make(chan struct{})
		chan_close    = make(chan struct{})
		chan_response = make(chan *Message)
	)

	if debug {
		self.DebugPrintln(
			"requestSendingRespWaitingRoutine.",
			"before self.jrpc_node.SendRequest",
		)
	}

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
		// is timeout specified by protocol of Multiplexer
		time.Minute,
		request_id_hook,
	)
	if debug {
		self.DebugPrintln(
			"requestSendingRespWaitingRoutine.",
			"after self.jrpc_node.SendRequest",
			timeout, closed, resp, proto_err, err,
		)
	}

	if err != nil {
		return false, false, nil, nil, err
	}

	select {
	case <-chan_timeout:
		if debug {
			self.DebugPrintln("timeout waiting for buffer info from peer")
		}
		if retry_countdown != 0 {
			retry_countdown--
			if debug {
				self.DebugPrintln("   retrying to get buffer info")
			}
			goto retry_label
		}
		return true, false, nil, nil, errors.New("timeout")
	case <-chan_close:
		if debug {
			self.DebugPrintln("waited for message from peer, but local node is closed")
		}
		return false, true, nil, nil, errors.New("node closed")
	case resp = <-chan_response:

		proto_err := resp.IsInvalidError()
		if proto_err != nil {
			return false, false, nil, proto_err, errors.New("protocol error")
		}

		return false, false, resp, nil, nil
	}
}

// no re-entrant locks in golang
func (self *JSONRPC2Multiplexer) getBuffByIdLocal(
	id string,
	lrc *golockerreentrancycontext.LockerReentrancyContext,
) (
	bw *JSONRPC2MultiplexerBufferWrapper,
	ok bool,
) {
	if lrc == nil {
		lrc = new(golockerreentrancycontext.LockerReentrancyContext)
	}

	if debug {
		defer self.DebugPrintln("getBuffByIdLocal - exited")
	}

	if debug {
		self.DebugPrintln("getBuffByIdLocal : before Lock()")
	}

	lrc.LockMutex(self.buffer_wrappers_mutex2)
	defer lrc.UnlockMutex(self.buffer_wrappers_mutex2)

	if debug {
		self.DebugPrintln("getBuffByIdLocal : after Lock()")
	}

	for _, x := range self.buffer_wrappers {
		if x.BufferId == id {
			return x, true
		}
	}
	return nil, false
}

func (self *JSONRPC2Multiplexer) jrpcOnRequestCB_NEW_BUFFER_AVAILABLE(
	msg *Message,
) (
	timedout bool,
	closed bool,
	proto_err error,
	err error,
) {
	if debug {
		self.DebugPrintln("jrpcOnRequestCB_NEW_BUFFER_AVAILABLE()")
	}

	var msg_par map[string]any

	msg_par, ok := (msg.Params).(map[string]any)
	if !ok {
		return false,
			false,
			errors.New("can't convert msg.Params to map[string]string"),
			errors.New("protocol error")
	}

	buffid_str, proto_err, err := getBuffIdFrom_msg_par(msg_par)
	if proto_err != nil || err != nil {
		return false, false, proto_err, err
	}

	if debug {
		self.DebugPrintfln("jrpcOnRequestCB_NEW_BUFFER_AVAILABLE(%s)", buffid_str)
	}

	var buffer_info_resp *JSONRPC2Multiplexer_proto_BufferInfo_Res

	{
		timedout, closed, buffer_info_resp, proto_err, err =
			self.getBuffInfo(buffid_str, time.Minute)

		if proto_err != nil || err != nil {
			return timedout, closed, proto_err, err
		}
	}

	// TODO: add error checks?

	// TODO: add size limit?

	buf_size := buffer_info_resp.Size

	if self.OnRequestToProvideWriteSeekerCB == nil {
		self.OnRequestToProvideWriteSeekerCB =
			self.DefaultOnRequestToProvideWriteSeekerCB
	}

	var write_seeker io.WriteSeeker

	err = self.OnRequestToProvideWriteSeekerCB(
		buf_size,
		func(ws io.WriteSeeker) error {
			ssize, err := ws.Seek(0, io.SeekEnd)
			if err != nil {
				return err
			}
			if ssize < buf_size {
				return errors.New(
					"must provide WriteSeeker size not less than reqired",
				)
			}
			write_seeker = ws
			return nil
		},
	)
	if err != nil {
		return false, false, nil, err
	}

	//result_buff := make([]byte, buf_size)

	const slice_size = 1024 // to surely fit into MTU size

	// buf_size_div_slice_size := buf_size / slice_size

	//iterations_count := buf_size_div_slice_size
	iterations_count := int64(buf_size / slice_size)
	last_size := buf_size - (slice_size * iterations_count)

	if debug {
		self.DebugPrintfln("jrpcOnRequestCB_NEW_BUFFER_AVAILABLE(%s)", buffid_str)
		self.DebugPrintfln("   buffer_info_resp.Size = %d", buffer_info_resp.Size)
		self.DebugPrintfln("   for i := 0; i != %d; i++ {", iterations_count)
		self.DebugPrintfln("   last_size = %d", last_size)
		// self.DebugPrintfln("   buf_size_div_slice_size = %d", buf_size_div_slice_size)
	}
	for i := int64(0); i != iterations_count; i++ {

		buff_start := slice_size * i
		buff_end := slice_size * (i + 1)

		retry_countdown := 3

	retry_label2:
		timedout, closed, proto_err, err := self.getBuffSlice(
			write_seeker,
			buffid_str,
			buff_start,
			buff_end,
			time.Minute,
		)

		if timedout || closed || proto_err != nil || err != nil {
			if retry_countdown != 0 {
				retry_countdown--
				goto retry_label2
			} else {
				return timedout, closed, proto_err, err
			}
		}
	}

	if debug {
		self.DebugPrintln("	if last_size > 0 { :", last_size)
	}

	if last_size > 0 {
		buff_start := slice_size * iterations_count
		buff_end := buff_start + last_size
		retry_countdown := 3

	retry_label3:
		if debug {
			self.DebugPrintln("retry_label3:")
		}
		timedout, closed, proto_err, err := self.getBuffSlice(
			write_seeker,
			buffid_str,
			buff_start,
			buff_end,
			time.Minute,
		)
		if debug {
			self.DebugPrintln("getBuffSlice result:", timedout, closed, proto_err, err)
		}

		if timedout || closed || proto_err != nil || err != nil {
			if retry_countdown != 0 {
				retry_countdown--
				goto retry_label3
			} else {
				return timedout, closed, proto_err, err
			}
		}
	}

	if debug {
		self.DebugPrintln("go self.OnIncommingDataTransferComplete(write_seeker)")
	}
	go self.OnIncommingDataTransferComplete(write_seeker)

	return false, false, nil, nil
}

func (self *JSONRPC2Multiplexer) jrpcOnRequestCB_GET_BUFFER_INFO(
	msg *Message,
	lrc *golockerreentrancycontext.LockerReentrancyContext,
) (
	timedout bool,
	closed bool,
	proto_err error,
	err error,
) {
	if lrc == nil {
		lrc = new(golockerreentrancycontext.LockerReentrancyContext)
	}
	if debug {
		self.DebugPrintln("jrpcOnRequestCB_GET_BUFFER_INFO")
	}

	var msg_par map[string]any

	msg_par, ok := (msg.Params).(map[string]any)
	if !ok {
		return false,
			false,
			errors.New("can't convert msg.Params to map[string]string"),
			errors.New("protocol error")
	}

	buffid_str, proto_err, err := getBuffIdFrom_msg_par(msg_par)
	if proto_err != nil || err != nil {
		return false, false, proto_err, err
	}

	if debug {
		self.DebugPrintln(
			"jrpcOnRequestCB_GET_BUFFER_INFO: other side requested info on buff with id",
			buffid_str,
		)
	}

	bw, ok := self.getBuffByIdLocal(buffid_str, lrc)
	if !ok {
		// fmt.Println("jrpcOnRequestCB: client tried to request unexisting buffer")
		return false,
			false,
			nil,
			errors.New("invalid buffer id")
	}

	info := new(JSONRPC2Multiplexer_proto_BufferInfo_Res)

	{
		buff_size, err := bw.BufferSize()
		if err != nil {
			return false, false, nil, errors.New("can't get buffer size")

		}
		info.Size = buff_size
	}

	// TODO: next not checked. thinking and checking required

	if debug {
		self.DebugPrintln("jrpcOnRequestCB_GET_BUFFER_INFO: composing response")
	}

	resp := new(Message)
	{
		x, ok := msg.GetId()
		if !ok {
			panic("this should be impossible")
		}
		err := resp.SetId(x)
		if err != nil {
			panic("this should be impossible:" + err.Error())
		}
	}
	resp.Response.Result = info
	resp.Error = nil

	if debug {
		self.DebugPrintln(
			"jrpcOnRequestCB_GET_BUFFER_INFO: before SendResponse ",
		)
	}
	err = self.jrpc_node.SendResponse(resp)
	if err != nil {
		if debug {
			self.DebugPrintln(
				"jrpcOnRequestCB_GET_BUFFER_INFO: SendResponse error:",
				err,
			)
		}
		return false, false, nil, err
	}

	if debug {
		self.DebugPrintln(
			"jrpcOnRequestCB_GET_BUFFER_INFO: after SendResponse ",
		)
	}

	return false, false, nil, nil
}

func (self *JSONRPC2Multiplexer) jrpcOnRequestCB_GET_BUFFER_SLICE(
	msg *Message,
	lrc *golockerreentrancycontext.LockerReentrancyContext,
) (
	timedout bool,
	closed bool,
	proto_err error,
	err error,
) {
	if lrc == nil {
		lrc = new(golockerreentrancycontext.LockerReentrancyContext)
	}
	if debug {
		self.DebugPrintln("jrpcOnRequestCB_GET_BUFFER_SLICE")
	}
	defer func() {
		if debug {
			self.DebugPrintln(
				"jrpcOnRequestCB_GET_BUFFER_SLICE defer:",
				timedout,
				closed,
				proto_err,
				err,
			)
		}
	}()

	var msg_par map[string]any

	msg_par, ok := (msg.Params).(map[string]any)
	if !ok {
		return false,
			false,
			errors.New("can't convert msg.Params to map[string]string"),
			errors.New("protocol error")
	}

	buffid_str, proto_err, err := getBuffIdFrom_msg_par(msg_par)
	if proto_err != nil || err != nil {
		return false, false, proto_err, err
	}

	var (
		start int64
		end   int64
	)

	{
		start_any, ok := msg_par["start"]
		if !ok {
			return false,
				false,
				errors.New("'start' parameter required, but not found"),
				errors.New("protocol error")
		}

		{
			start_any_cloat, ok := start_any.(float64)
			if !ok {
				return false,
					false,
					errors.New("can't convert 'start' to int"),
					errors.New("protocol error")
			}
			start = int64(start_any_cloat)
		}

		end_any, ok := msg_par["end"]
		if !ok {
			return false,
				false,
				errors.New("'end' parameter required, but not found"),
				errors.New("protocol error")
		}
		end = int64(end_any.(float64))
	}

	if start < 0 {
		return false,
			false,
			errors.New("invalid value for 'start' parameter"),
			errors.New("protocol error")
	}

	if end < start {
		return false,
			false,
			errors.New("invalid value for 'end' parameter"),
			errors.New("protocol error")
	}

	var buff_slice []byte

	timedout, closed, proto_err, err = func() (bool, bool, error, error) {
		lrc.LockMutex(self.buffer_wrappers_mutex2)
		defer lrc.UnlockMutex(self.buffer_wrappers_mutex2)

		buff, ok := self.getBuffByIdLocal(
			buffid_str,
			lrc,
		)
		if !ok {
			return false,
				false,
				nil,
				errors.New("buffer not found")
		}

		buff_size, err := buff.BufferSize()
		if err != nil {
			return false, false, nil, err
		}

		if end > buff_size {
			return false,
				false,
				errors.New("'end' is behind buffer"),
				errors.New("protocol error")
		}

		if debug {
			self.DebugPrintln("jrpcOnRequestCB_GET_BUFFER_SLICE. before BufferSlice:", start, end)
		}
		buff_slice, err = buff.BufferSlice(start, end)
		if err != nil {
			return false, false, nil, err
		}
		if debug {
			self.DebugPrintln("jrpcOnRequestCB_GET_BUFFER_SLICE. after BufferSlice:", buff_slice, err)
		}
		return false, false, nil, nil
	}()
	if proto_err != nil || err != nil {
		return false, false, proto_err, err
	}

	if debug {
		self.DebugPrintln("jrpcOnRequestCB_GET_BUFFER_SLICE: base64.RawStdEncoding.EncodeToString")
	}

	resp_msg := new(JSONRPC2Multiplexer_proto_BufferSlice_Res)
	resp_msg.Data = base64.RawStdEncoding.EncodeToString(buff_slice)

	// TODO: next not checked. thinking and checking required

	if debug {
		self.DebugPrintln("jrpcOnRequestCB_GET_BUFFER_SLICE: composing response")
	}

	resp := new(Message)
	{
		x, ok := msg.GetId()
		if !ok {
			panic("this should be impossible")
		}
		err := resp.SetId(x)
		if err != nil {
			panic("this should be impossible:" + err.Error())
		}
	}
	resp.Response.Result = resp_msg
	resp.Error = nil

	if debug {
		self.DebugPrintln(
			"jrpcOnRequestCB_GET_BUFFER_SLICE: before SendResponse ",
		)
	}
	err = self.jrpc_node.SendResponse(resp)
	if err != nil {
		if debug {
			self.DebugPrintln(
				"jrpcOnRequestCB_GET_BUFFER_SLICE: SendResponse error:",
				err,
			)
		}
		return false, false, nil, err
	}

	if debug {
		self.DebugPrintln(
			"jrpcOnRequestCB_GET_BUFFER_SLICE: after SendResponse ",
		)
	}

	return false, false, nil, nil
}

func (self *JSONRPC2Multiplexer) handle_jrpcOnRequestCB(
	msg *Message,
	lrc *golockerreentrancycontext.LockerReentrancyContext,
) (
	timeout bool,
	closed bool,
	proto_err error,
	err error,
) {
	if lrc == nil {
		lrc = new(golockerreentrancycontext.LockerReentrancyContext)
	}

	// if !msg.IsRequestAndNotNotification() {
	// 	// TODO: report errors
	// }

	var dont_send_default bool = false

	resp := new(Message)

	defer func() {
		// TODO: add error handling?
		if dont_send_default {
			if debug {
				self.DebugPrintln(
					"handle_jrpcOnRequestCB: dont_send_default ==",
					dont_send_default,
				)
			}
		} else {
			var err2 error
			if debug {
				var b []byte
				b, err2 = json.Marshal(resp)
				if err2 != nil {
					timeout = false
					closed = false
					proto_err = nil
					err = err2
					return
				}
				self.DebugPrintln("handle_jrpcOnRequestCB: sending response:", string(b))
			}
			err2 = self.jrpc_node.SendMessage(resp)
			if err2 != nil {
				timeout = false
				closed = false
				proto_err = nil
				err = err2
				if debug {
					self.DebugPrintln("handle_jrpcOnRequestCB: error on trying to send response:", err)
				}
			}
		}
	}()

	resp.Error = &JSONRPC2Error{
		Code:    -32603,
		Message: "Internal error",
	}

	if debug {
		self.DebugPrintln("handle_jrpcOnRequestCB: msg.Method:", msg.Method)
	}

	resp.Id = msg.Id
	switch msg.Method {
	default:
		if debug {
			self.DebugPrintln("handle_jrpcOnRequestCB: case default")
		}
		proto_err = errors.New("peer requested unsupported Method")
		err = errors.New("protocol error")
		resp.Error.Code = -32000
		resp.Error.Message = "protocol error"
		return

	case JSONRPC2_MULTIPLEXER_METHOD_NEW_BUFFER_AVAILABLE:
		if debug {
			self.DebugPrintln(
				"handle_jrpcOnRequestCB:" +
					" case JSONRPC2_MULTIPLEXER_METHOD_NEW_BUFFER_AVAILABLE",
			)
		}
		timeout, closed, proto_err, err =
			self.jrpcOnRequestCB_NEW_BUFFER_AVAILABLE(msg)
		if proto_err != nil {
			resp.Error.Code = -32000
			resp.Error.Message = "protocol error"
		}
		if debug {
			if proto_err != nil || err != nil {
				self.DebugPrintln(
					"handle_jrpcOnRequestCB "+
						"(JSONRPC2_MULTIPLEXER_METHOD_NEW_BUFFER_AVAILABLE):"+
						" errors:", proto_err, ":", err,
				)
			}
		}

		if proto_err != nil || err != nil {
			return
		} else {
			dont_send_default = true
			return
		}

	case JSONRPC2_MULTIPLEXER_METHOD_GET_BUFFER_INFO:
		if debug {
			self.DebugPrintln(
				"handle_jrpcOnRequestCB:" +
					" case JSONRPC2_MULTIPLEXER_METHOD_GET_BUFFER_INFO",
			)
		}
		// TODO: reset timeout for
		// JSONRPC2_MULTIPLEXER_METHOD_NEW_BUFFER_AVAILABLE request
		timeout, closed, proto_err, err =
			self.jrpcOnRequestCB_GET_BUFFER_INFO(msg, lrc)
		if proto_err != nil {
			resp.Error.Code = -32000
			resp.Error.Message = "protocol error"
		}
		if debug {
			if proto_err != nil || err != nil {
				self.DebugPrintln(
					"handle_jrpcOnRequestCB "+
						"(JSONRPC2_MULTIPLEXER_METHOD_GET_BUFFER_INFO):"+
						" errors:", proto_err, ":", err,
				)
			}
		}

		if proto_err != nil || err != nil {
			return
		} else {
			dont_send_default = true
			return
		}

	case JSONRPC2_MULTIPLEXER_METHOD_GET_BUFFER_SLICE:
		if debug {
			self.DebugPrintln(
				"handle_jrpcOnRequestCB:" +
					" case JSONRPC2_MULTIPLEXER_METHOD_GET_BUFFER_SLICE",
			)
		}
		// TODO: reset timeout for
		// JSONRPC2_MULTIPLEXER_METHOD_NEW_BUFFER_AVAILABLE request
		timeout, closed, proto_err, err =
			self.jrpcOnRequestCB_GET_BUFFER_SLICE(msg, lrc)
		if proto_err != nil {
			resp.Error.Code = -32000
			resp.Error.Message = "protocol error"
		}
		if debug {
			if proto_err != nil || err != nil {
				self.DebugPrintln(
					"handle_jrpcOnRequestCB "+
						"(JSONRPC2_MULTIPLEXER_METHOD_GET_BUFFER_SLICE):"+
						" errors:", proto_err, ":", err,
				)
			}
		}

		if proto_err != nil || err != nil {
			return
		} else {
			dont_send_default = true
			return
		}
	}

}

// results:
// #0 bool - timedout
// #1 bool - closed
// #2 *JSONRPC2Multiplexer_proto_BufferInfo
// #2 invalid response (protocol) error - not nil in case if it's protocol error
// #3 error
func (self *JSONRPC2Multiplexer) getBuffInfo(
	buffid string,
	timeout time.Duration,
) (bool, bool, *JSONRPC2Multiplexer_proto_BufferInfo_Res, error, error) {
	m := new(Message)
	m.Method = JSONRPC2_MULTIPLEXER_METHOD_GET_BUFFER_INFO
	m.Params = map[string]string{"id": buffid}
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
			errors.New("protocol error")
	}

	// TODO: checks required

	resp_map_s, ok := resp_map["s"]
	if !ok {
		return false,
			false,
			nil,
			errors.New("can't get 's' value from json object"),
			errors.New("protocol error")
	}

	resp_map_s_float64, ok := resp_map_s.(float64)
	if !ok {
		return false,
			false,
			nil,
			errors.New("can't read 's' float value from json object"),
			errors.New("protocol error")
	}

	var resp_map_s_int64 int64

	{
		x1, x2 := math.Modf(resp_map_s_float64)

		if x2 != 0 {
			return false,
				false,
				nil,
				errors.New("can't interpret 's' value from json object as integer"),
				errors.New("protocol error")
		}

		resp_map_s_int64 = int64(x1)

		if resp_map_s_int64 < 0 {
			return false,
				false,
				nil,
				errors.New("buffer size must be positive"),
				errors.New("protocol error")
		}

		// note: no restriction to maximum buffer size - yes?
		// if resp_map_s_int64 > (2 * 1024 * 1024) {
		// 	return false,
		// 		false,
		// 		nil,
		// 		errors.New("buffer is too large"),
		// 		errors.New("protocol error")
		// }
	}

	ret := new(JSONRPC2Multiplexer_proto_BufferInfo_Res)
	ret.Size = resp_map_s_int64
	if debug {
		self.DebugPrintln("ret.Size:", ret.Size)
	}
	if !ok {
		return false,
			false,
			nil,
			errors.New("couldn't get buff size value as integer from response"),
			errors.New("protocol error")
	}

	return false, false, ret, nil, nil
}

// results:
// #0 bool - timeout
// #1 bool - closed
// #2 invalid response (protocol) error - not nil in case if it's protocol error
// #3 error
func (self *JSONRPC2Multiplexer) getBuffSlice(
	target_buff io.WriteSeeker,
	buffid string,
	buff_start int64,
	buff_end int64,
	timeout time.Duration,
) (
	timedout bool,
	closed bool,
	proto_err error,
	err error,
) {

	// todo: is this func a thread-safe?

	if debug {
		self.DebugPrintln("getBuffSlice: starting")
		self.DebugPrintln("getBuffSlice: buffid", buffid)
		self.DebugPrintln("getBuffSlice: start:", buff_start, ", end:", buff_end)
	}
	defer func() {
		xxx := recover()
		if xxx != nil {
			self.DebugPrintln("getBuffSlice panic:", xxx)
			panic(xxx)
		}

		if debug {
			self.DebugPrintln("getBuffSlice: defer exit:",
				timedout,
				closed,
				proto_err,
				err,
			)
		}
	}()

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
		m.Method = JSONRPC2_MULTIPLEXER_METHOD_GET_BUFFER_SLICE
		p := &JSONRPC2Multiplexer_proto_BufferSlice_Req{
			JSONRPC2Multiplexer_proto_NewBufferMsg: JSONRPC2Multiplexer_proto_NewBufferMsg{
				BufferId: buffid,
			},
			Start: buff_start,
			End:   buff_end,
		}
		m.Params = p

		var (
			timedout   bool
			closed     bool
			proto_eror error
			err        error
		)

		if debug {
			self.DebugPrintln(
				"getBuffSlice: requestSendingRespWaitingRoutine",
			)
		}

		timedout, closed, resp_msg, proto_eror, err =
			self.requestSendingRespWaitingRoutine(m, nil)

		if debug {
			self.DebugPrintln(
				"getBuffSlice: requestSendingRespWaitingRoutine result",
				timedout, closed, resp_msg, proto_eror, err,
			)
		}

		if proto_eror != nil || err != nil {
			return timedout, closed, proto_eror, err
		}
	}

	if !resp_msg.IsError() {
		var val map[string]any
		val, ok := resp_msg.Result.(map[string]any)
		if !ok {
			return false, false,
				errors.New("can't use result as json object"),
				errors.New("protocol error")
		}

		val_data, ok := val["data"]
		if !ok {
			return false, false,
				errors.New("can't get 'data' from json object"),
				errors.New("protocol error")
		}

		val_data_str, ok := val_data.(string)
		if !ok {
			return false, false,
				errors.New("can't use 'data' from json object as string"),
				errors.New("protocol error")
		}

		val_b, err := base64.RawStdEncoding.DecodeString(val_data_str)
		if err != nil {
			return false, false, nil, err
		}

		len_b := len(val_b)

		if len_b != int(control_size) {
			return false, false,
				errors.New("peer returned buffer with invalid size"),
				errors.New("protocol error")
		}

		_, err = target_buff.Seek(buff_start, io.SeekStart)
		if err != nil {
			return false, false, nil, err
		}

		buff_bytes := bytes.NewBuffer(val_b)

		_, err = io.CopyN(target_buff, buff_bytes, int64(buff_bytes.Len()))
		// todo: also check 'copied' result?
		if err != nil {
			return false, false, nil, err
		}

	}

	return false, false, nil, nil
}

func (self *JSONRPC2Multiplexer) jrpcPushMessageToOutsideCB(data []byte) error {
	return self.PushMessageToOutsideCB(data)
}

// use this function to send the data. this function will not return until
// send succeed or fail
func (self *JSONRPC2Multiplexer) ChannelData(data io.ReadSeeker) (
	timedout bool,
	closed bool,
	resp_msg *Message,
	proto_err error,
	err error,
) {

	lrc := new(golockerreentrancycontext.LockerReentrancyContext)

	if debug {
		self.DebugPrintln("got data to channel:", data)
	}

	var buffer_id string
	var request_id any

	wrapper := new(JSONRPC2MultiplexerBufferWrapper)

	func() {
		lrc.LockMutex(self.buffer_wrappers_mutex2)
		defer lrc.UnlockMutex(self.buffer_wrappers_mutex2)

		if debug {
			self.DebugPrintln("generating id for new buffer")
		}

		buffer_id, err = self.genUniqueBufferId(lrc)
		if err != nil {
			return
		}

		if debug {
			self.DebugPrintln("new id for buffer:", buffer_id)
		}

		wrapper.BufferId = buffer_id
		wrapper.Buffer = data

		if debug {
			self.DebugPrintln("saving buffer", buffer_id, "to wrapper")
		}
		self.buffer_wrappers = append(self.buffer_wrappers, wrapper)
	}()
	if err != nil {
		return false, false, nil, nil, err
	}

	defer func() {
		lrc.LockMutex(self.buffer_wrappers_mutex2)
		defer lrc.UnlockMutex(self.buffer_wrappers_mutex2)

		if debug {
			self.DebugPrintln("cleaning up buffer", buffer_id, "wrapper")
		}

		for i := len(self.buffer_wrappers) - 1; i != -1; i += -1 {
			if self.buffer_wrappers[i].BufferId == buffer_id {
				self.buffer_wrappers = append(
					self.buffer_wrappers[:i],
					self.buffer_wrappers[i+1:]...,
				)
			}
		}
	}()

	new_buffer_msg := new(JSONRPC2Multiplexer_proto_NewBufferMsg)
	new_buffer_msg.BufferId = buffer_id

	channel_start_msg := new(Message)
	channel_start_msg.Method = JSONRPC2_MULTIPLEXER_METHOD_NEW_BUFFER_AVAILABLE
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
		if debug {
			self.DebugPrintln("waiting for new request id")
		}
		request_id = <-new_id_chan
		lrc.LockMutex(self.buffer_wrappers_mutex2)
		defer lrc.UnlockMutex(self.buffer_wrappers_mutex2)
		wrapper.RequestId = request_id
		if debug {
			self.DebugPrintln("new request id is:", request_id)
		}
		if debug {
			self.DebugPrintln("sending continue signal")
		}
		continue_chan <- struct{}{}
	}()

	if debug {
		self.DebugPrintln("sending new request")
	}
	timedout, closed, resp_msg, proto_err, err =
		self.requestSendingRespWaitingRoutine(
			channel_start_msg,
			hook,
		)
	if proto_err != nil || err != nil {
		return false, false, nil, proto_err, err
	}

	if debug {
		self.DebugPrintln(
			"request sending results:",
			timedout, closed, resp_msg, proto_err, err,
		)
	}

	return false, false, resp_msg, nil, nil
}

// this have protocol restriction on input data size
// #0 - protocol error
// #1 - all errors
func (self *JSONRPC2Multiplexer) PushMessageFromOutside(data []byte) (error, error) {
	if len(data) >= 1050 {
		return errors.New("data is too big. must be < 1050"),
			errors.New("protocol error")
	}
	return self.jrpc_node.PushMessageFromOutside(data)
}

// TODO: ?
// func (self *JSONRPC2Multiplexer) PushMessage(data []byte) (error, error) {
// 	return nil, nil
// }

func (self *JSONRPC2Multiplexer) genUniqueBufferId(
	lrc *golockerreentrancycontext.LockerReentrancyContext,
) (string, error) {
	if lrc == nil {
		lrc = new(golockerreentrancycontext.LockerReentrancyContext)
	}

	var ret string

	for true {
		if debug {
			self.DebugPrintln("generating new UUID")
		}
		u := uuid.NewV4()

		ret = u.String()

		if debug {
			self.DebugPrintln(ret)
		}

		if debug {
			self.DebugPrintln("testing")
		}
		_, ok := (self.getBuffByIdLocal(ret, lrc))
		if !ok {
			if debug {
				self.DebugPrintln("doesn't exists already - ok")
			}
			break
		}
		if debug {
			self.DebugPrintln("retry")
		}
	}

	return ret, nil
}

func getBuffIdFrom_msg_par(
	msg_par map[string]any,
) (
	buffid_str string,
	proto_err error,
	err error,
) {
	buffid, ok := msg_par["id"]
	if !ok {
		return "",
			errors.New("id parameter not found"),
			errors.New("protocol error")
	}

	buffid_str, ok = buffid.(string)
	if !ok {
		return "",
			nil,
			errors.New("can't convert buffid to string")
	}
	return buffid_str, nil, nil
}
