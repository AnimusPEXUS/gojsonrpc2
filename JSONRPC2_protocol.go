package gojsonrpc2

import (
	"encoding/json"
	"errors"
	"reflect"
)

type JSONRPC2Field struct {
	JSONRPC string `json:"jsonrpc"`
}

type IdField struct {
	Id any `json:"id"`
}

type Request struct {
	Method string `json:"method,omitempty"`
	Params any    `json:"params,omitempty"`
}

type Response struct {
	Result any            `json:"result,omitempty"`
	Error  *JSONRPC2Error `json:"error,omitempty"`
}

type JSONRPC2Error struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
	Data    any    `json:"data,omitempty"`
}

type Message struct {
	JSONRPC2Field
	IdField
	Request
	Response
}

func (val *Message) checkJSONRPC2field() error {
	if val.JSONRPC != "2.0" {
		return errors.New("invalid 'jsonrpc' version")
	}
	return nil
}

func (self *Message) HaveId() bool       { return !(self.Id == nil) }
func (self *Message) DelId()             { self.Id = nil }
func (self *Message) GetId() (any, bool) { return self.Id, self.HaveId() }

func (self *Message) SetId(val any) error {
	var v = reflect.ValueOf(val)
	if v.IsNil() {
		self.DelId()
	} else if x := v.Kind(); x != reflect.String && x != reflect.Int {
		return errors.New("invalid value type for 'id'")
	} else {
		self.Id = val
	}
	return nil
}

func NewRequestFromString(text string) (*Message, error) {

	var res map[string]any
	err := json.Unmarshal([]byte(text), &res)
	if err != nil {
		return nil, err
	}

	return NewRequestFromAny(res)
}

func NewResponseFromString(text string) (*Message, error) {

	var res map[string]any
	err := json.Unmarshal([]byte(text), &res)
	if err != nil {
		return nil, err
	}

	return NewResponseFromAny(res)
}

func NewRequestFromAny(data any) (*Message, error) {

	res, ok := data.(map[string]any)
	if !ok {
		return nil, errors.New("can't treat supplied data as map[string]any")
	}

	var ret = new(Message)

	{
		v, ok := res["id"]

		if ok {
			err := ret.SetId(v)
			if err != nil {
				return nil, err
			}
		}
	}

	{
		v, ok := res["method"]
		if !ok {
			return nil, errors.New("invalid request object: no method")
		}

		vv := reflect.ValueOf(v)

		if vv.Kind() != reflect.String {
			return nil, errors.New("invalid request object: no method")
		}
	}

	err := ret.IsInvalid()
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func NewResponseFromAny(data any) (*Message, error) {

	res, ok := data.(map[string]any)
	if !ok {
		return nil, errors.New("can't treat supplied data as map[string]any")
	}

	var ret = new(Message)

	{
		v, ok := res["id"]

		if !ok {
			return nil, errors.New("id not found. can't be treated as response")
		}

		err := ret.SetId(v)
		if err != nil {
			return nil, err
		}
	}

	{
		ret.Result = res["result"]

		res_err, ok := res["error"]
		if ok {
			var res_err_inter map[string]any
			res_err_inter, ok = res_err.(map[string]any)

			er := new(JSONRPC2Error)
			res_err_inter_code, ok := res_err_inter["code"]
			if !ok {
				return nil, errors.New("response have error object, but have no code field")
			}
			er.Code, ok = res_err_inter_code.(int)

			res_err_inter_message, ok := res_err_inter["message"]
			if !ok {
				return nil, errors.New("response have error object, but have no message field")
			}
			er.Message = res_err_inter_message.(string)

			er.Data = res_err_inter["data"]
		}
	}

	err := ret.IsInvalid()
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (self *Message) HasRequestFields() bool {
	return self.Method != ""
}

func (self *Message) HasResponseFields() bool {
	return self.Result != nil || self.Error != nil
}

func (self *Message) IsInvalid() error {
	is_request := self.HasRequestFields()
	is_response := self.HasResponseFields()

	if is_response && is_request {
		return errors.New("have both request and response fields")
	}

	if !is_response && !is_request {
		return errors.New("doesn't have both request and response fields")
	}

	if is_response {
		if !self.HaveId() {
			return errors.New("response and have no id field")
		}

		if self.Result != nil && self.Error != nil {
			return errors.New("response have both result and error fields")
		}
	}

	return nil
}

func (self *Message) IsError() bool {
	err := self.IsInvalid()
	if err != nil {
		return false
	}

	if !self.HasResponseFields() {
		return false
	}

	return self.Error != nil
}
