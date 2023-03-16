package gojsonrpc2

// protocol part for JSONRPC2Multiplexer. defines message structures

type JSONRPC2Multiplexer_proto_NewBufferMsg struct {
	BufferId string `json:"id"`
}

type JSONRPC2Multiplexer_proto_BufferInfo_Req struct {
	JSONRPC2Multiplexer_proto_NewBufferMsg
}

type JSONRPC2Multiplexer_proto_BufferInfo_Res struct {
	Size int64 `json:"s"`
}

type JSONRPC2Multiplexer_proto_BufferSlice_Req struct {
	JSONRPC2Multiplexer_proto_NewBufferMsg
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

type JSONRPC2Multiplexer_proto_BufferSlice_Res struct {
	Data string `json:"data"` // base64 encoded
}
