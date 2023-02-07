package main

import (
	"encoding/json"
	"fmt"

	"github.com/AnimusPEXUS/gojsonrpc2"
)

func main() {

	// j1 := gojsonrpc2.NewJSONRPC2Node()

	// j2 := gojsonrpc2.NewJSONRPC2Node()

	c1 := gojsonrpc2.NewJSONRPC2Channeler()

	c2 := gojsonrpc2.NewJSONRPC2Channeler()

	// j1.PushMessageToOutsideCB = func(data []byte) error {
	// 	err := c1.ChannelData(data)
	// 	if err != nil {
	// 		return err
	// 	}
	// 	return nil
	// }

	c1.PushMessageToOutsideCB = func(data []byte) error {
		c2.PushMessageFromOutside(data)
		return nil
	}

	c2.PushMessageToOutsideCB = func(data []byte) error {
		c1.PushMessageFromOutside(data)
		return nil
	}

	c2.OnDataCB = func(data []byte) {
		fmt.Println("got new data: ", data)
	}

	msg := new(gojsonrpc2.Message)
	msg.Method = "mtdnm"
	msg.Params = map[string]any{
		"param1": 123,
		"param2": 456,
	}

	b, err := json.Marshal(msg)
	if err != nil {
		fmt.Println(err)
		return
	}

	timedout, closed, _, proto_err, err := c1.ChannelData(b)
	if proto_err != nil || err != nil {
		fmt.Println("proto_err:", proto_err)
		fmt.Println("err      :", err)
		return
	}

	fmt.Println(timedout)
	fmt.Println(closed)

}
