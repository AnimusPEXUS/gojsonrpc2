package gojsonrpc2

// protocol part for JSONRPC2Channeler. defines message structures

type JSONRPC2Channeler_proto_NewBufferMsg struct {
	BufferId string `json:"id"`
}

type JSONRPC2Channeler_proto_Buffer struct {
	Data []byte
}

type JSONRPC2Channeler_proto_BufferInfo struct {
	Size int `json:"s"`
}
