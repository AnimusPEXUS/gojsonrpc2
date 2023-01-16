package gojsonrpc2

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	uuid "github.com/satori/go.uuid"
)

const (
	JSONRPC2_CHANNELER_METHOD_NEW_BUFFER_AVAILABLE = "n"
	JSONRPC2_CHANNELER_METHOD_GET_BUFFER_INFO      = "gbi"
	JSONRPC2_CHANNELER_METHOD_GET_BUFFER_SLICE     = "gbs"
)

type JSONRPC2Channeler struct {
	PushMessageToOutsideCB func(data []byte) error

	OnData              func(data []byte)
	OnPeerProtocolError func(error)

	buffers       map[string]([]byte)
	buffers_mutex *sync.Mutex

	jrpc_node *JSONRPC2Node
}

func NewJSONRPC2Channeler() *JSONRPC2Channeler {
	self := new(JSONRPC2Channeler)

	self.buffers_mutex = new(sync.Mutex)

	self.jrpc_node = NewJSONRPC2Node()
	self.jrpc_node.OnRequestCB = self.jrpcOnRequestCB
	self.jrpc_node.PushMessageToOutsideCB = self.jrpcPushMessageToOutsideCB

	return self
}

func (self *JSONRPC2Channeler) Close() {
	self.jrpc_node.Close()
	self.jrpc_node = nil
	self.buffers = nil
}

func (self *JSONRPC2Channeler) jrpcOnRequestCB(msg *Message) {
	if msg.HaveId() {
		func() {
			resp := new(Message)

			defer func() {
				// TODO: add error handling
				self.jrpc_node.SendMessage(resp)
			}()

			resp.Error = &JSONRPC2Error{
				Code:    -32603,
				Message: "Internal error",
			}

			resp.Id = msg.Id
			switch msg.Method {
			default:
				if self.OnPeerProtocolError != nil {
					self.OnPeerProtocolError(
						errors.New("peer requested unsupported Method"),
					)
				}
				return
			case JSONRPC2_CHANNELER_METHOD_NEW_BUFFER_AVAILABLE:
				var msg_par map[string]string

				msg_par, ok := (msg.Params).(map[string]string)
				if !ok {
					if self.OnPeerProtocolError != nil {
						self.OnPeerProtocolError(
							errors.New("can't convert msg.Params to map[string]string"),
						)
					}
					// TODO: set resp error message?
					return
				}

				buffid, ok := msg_par["buffid"]
				if !ok {
					if self.OnPeerProtocolError != nil {
						self.OnPeerProtocolError(
							errors.New("jrpcOnRequestCB: buffid parameter not found"),
						)
					}
					// TODO: set resp error message?
					return
				}

				var buffer_info_resp *Message

				{
					retry_countdown := 3
				retry_label:

					var (
						chan_timeout  = make(chan struct{})
						chan_close    = make(chan struct{})
						chan_response = make(chan *Message)
					)

					m := new(Message)
					m.Method = JSONRPC2_CHANNELER_METHOD_GET_BUFFER_INFO
					m.Params = map[string]string{"buffid": buffid}
					// TODO: error checks?
					self.jrpc_node.SendRequest(
						m,
						true,
						false,
						&JSONRPC2NodeRespHandler{
							OnTimeout: func() {
								chan_timeout <- struct{}{}
							},
							OnClose: func() {
								chan_close <- struct{}{}
							},
							OnResponse: func(resp *Message) {
								chan_response <- resp
							},
						},
						time.Minute,
					)

					select {
					case <-chan_timeout:
						fmt.Println("timeout waiting for buffer info from peer")
						if retry_countdown != 0 {
							retry_countdown--
							fmt.Println("   retrying to get buffer info")
							goto retry_label
						}
						// TODO: report
						return
					case <-chan_close:
						fmt.Println("waited for message from peer, but local node is closed")
						return
					case buffer_info_resp = <-chan_response:
						break
					}
				}

				// TODO: add error checks

				// TODO: add size limit

				buf_size := (buffer_info_resp.Result).(map[string]any)["size"].(int)

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
					timedout, closed, _, _, err := self.getBuffSlice(
						result_buff,
						buffid,
						buff_start,
						buff_end,
						time.Minute,
					)
					if err != nil {
						// TODO: report
						return
					}

					if closed {
						// TODO: report
						return
					}

					if timedout {
						if retry_countdown != 0 {
							retry_countdown--
							goto retry_label2
						}
						// TODO: report
						return
					}
				}

				if last_size > 0 {
					buff_start := slice_size * iterations_count
					buff_end := buff_start + last_size
					retry_countdown := 3

				retry_label3:
					timedout, closed, _, _, err := self.getBuffSlice(
						result_buff,
						buffid,
						buff_start,
						buff_end,
						time.Minute,
					)
					if err != nil {
						// TODO: report
						return
					}

					if closed {
						// TODO: report
						return
					}

					if timedout {
						if retry_countdown != 0 {
							retry_countdown--
							goto retry_label3
						}
						// TODO: report
						return
					}
				}

				go self.OnData(result_buff)

				return

			case JSONRPC2_CHANNELER_METHOD_GET_BUFFER_INFO:
				var msg_par map[string]string

				msg_par, ok := (msg.Params).(map[string]string)
				if !ok {
					if self.OnPeerProtocolError != nil {
						self.OnPeerProtocolError(
							errors.New("jrpcOnRequestCB: can't convert msg.Params to map[string]string"),
						)
					}
					// TODO: set resp error message?
					return
				}

				id, ok := msg_par["buffid"]
				if !ok {
					if self.OnPeerProtocolError != nil {
						self.OnPeerProtocolError(
							errors.New("jrpcOnRequestCB: buffid parameter not found"),
						)
					}
					// TODO: set resp error message?
					return
				}

				_, ok = self.buffers[id]
				if !ok {
					fmt.Println("jrpcOnRequestCB: client tried to request unexisting buffer")
					resp.Error.Code = -32700
					resp.Error.Message = "invalid buffer id"
					return
				}

				info := new(JSONRPC2ChannelerBufferInfo)
				info.Size = len(self.buffers[id])
				resp.Response.Result = info
				resp.Error = nil

				self.jrpc_node.SendResponse(resp)
				return
			}
		}()
	}
}

// results:
// #0 bool - timeout
// #1 bool - closed
// #2 *Message - response
// #3 invalid response (protocol) error - not nil in case if it's protocol error
// #4 error
func (self *JSONRPC2Channeler) getBuffSlice(
	target_buff []byte,
	buffid string,
	buff_start int,
	buff_end int,
	timeout time.Duration,
) (bool, bool, *Message, error, error) {

	var (
		chan_timeout  = make(chan struct{})
		chan_close    = make(chan struct{})
		chan_response = make(chan *Message)
	)

	if buff_start < 0 {
		return false, false, nil, nil, errors.New("invalid values for buff_start")
	}

	if buff_start > buff_end {
		return false, false, nil, nil, errors.New("invalid values for buff_start/buff_end")
	}

	control_size := buff_end - buff_start

	m := new(Message)
	m.Method = JSONRPC2_CHANNELER_METHOD_GET_BUFFER_SLICE
	m.Params = map[string]any{
		"buffid": buffid,
		"start":  buff_start,
		"end":    buff_end,
	}

	_, err := self.jrpc_node.SendRequest(
		m,
		true,
		false,
		&JSONRPC2NodeRespHandler{
			OnTimeout: func() {
				chan_timeout <- struct{}{}
			},
			OnClose: func() {
				chan_close <- struct{}{}
			},
			OnResponse: func(resp *Message) {
				chan_response <- resp
			},
		},
		timeout,
	)
	if err != nil {
		return false, false, nil, nil, err
	}

	var resp_msg *Message

	select {
	case <-chan_timeout:
		return true, false, nil, nil, nil
	case <-chan_close:
		return false, true, nil, nil, nil
	case resp_msg = <-chan_response:
		break
	}

	err = resp_msg.IsInvalid()
	if err != nil {
		return false, false, nil, err, errors.New("protocol error")
	}

	if !resp_msg.IsError() {
		val := resp_msg.Result.(string)
		val_b, err := base64.StdEncoding.DecodeString(val)
		if err != nil {
			return false, false, nil, nil, err
		}

		len_b := len(val_b)

		if len_b != control_size {
			return false, false, nil,
				errors.New("peer returned buffer with invalid size"),
				errors.New("protocol error")
		}

		copy(target_buff[buff_start:], val_b)

	}

	return false, false, resp_msg, nil, nil

}

func (self *JSONRPC2Channeler) jrpcPushMessageToOutsideCB(
	data []byte,
) error {
	self.PushMessageToOutsideCB(data)
	// TODO: handle errors?
	return nil
}

// use this function to transmit the data
func (self *JSONRPC2Channeler) ChannelData(data []byte) error {

	var err error

	id, err := self.genUniqueId()
	if err != nil {
		return err
	}

	// notify other side about new message presence
	channel_start_msg := new(Message)
	channel_start_msg.Method = JSONRPC2_CHANNELER_METHOD_NEW_BUFFER_AVAILABLE
	channel_start_msg.Params = map[string]string{"id": id}

	return nil
}

// #0 - protocol error
// #1 - all errors
func (self *JSONRPC2Channeler) PushMessageFromOutside(data []byte) (error, error) {
	return nil, nil
}

func (self *JSONRPC2Channeler) PushMessage(data []byte) (error, error) {
	return nil, nil
}

func (self *JSONRPC2Channeler) genUniqueId() (string, error) {
	var ret string

	for true {
		u, err := uuid.NewV4()
		if err != nil {
			return "", err
		}
		ret = strings.ToLower(u.String())

		_, ok := (self.buffers[ret])
		if ok {
			continue
		}
	}

	return ret, nil
}

type JSONRPC2ChannelerBuffer struct {
}

type JSONRPC2ChannelerBufferInfo struct {
	Size int `json:"s"`
}
