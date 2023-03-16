package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/AnimusPEXUS/goinmemfile"
	"github.com/AnimusPEXUS/gojsonrpc2"
)

func main() {

	// j1 := gojsonrpc2.NewJSONRPC2Node()

	// j2 := gojsonrpc2.NewJSONRPC2Node()

	c1 := gojsonrpc2.NewJSONRPC2Multiplexer()
	c1.SetDebugName("JSONRPC2Multiplexer c1")

	c2 := gojsonrpc2.NewJSONRPC2Multiplexer()
	c2.SetDebugName("JSONRPC2Multiplexer c2")

	// j1.PushMessageToOutsideCB = func(data []byte) error {
	// 	err := c1.ChannelData(data)
	// 	if err != nil {
	// 		return err
	// 	}
	// 	return nil
	// }

	c1.PushMessageToOutsideCB = func(data []byte) error {
		fmt.Println("c1.PushMessageToOutsideCB")
		go func(data2 []byte) {
			proto_err, err := c2.PushMessageFromOutside(data2)
			if proto_err != nil || err != nil {
				fmt.Println("c1.PushMessageToOutsideCB errors:", proto_err, "::", err)
				return
			}
			fmt.Println("c1.PushMessageToOutsideCB ok")
		}(data)
		return nil
	}

	c2.PushMessageToOutsideCB = func(data []byte) error {
		fmt.Println("c2.PushMessageToOutsideCB")
		go func(data2 []byte) {
			proto_err, err := c1.PushMessageFromOutside(data2)
			if proto_err != nil || err != nil {
				fmt.Println("c2.PushMessageToOutsideCB errors:", proto_err, "::", err)
				return
			}
			fmt.Println("c2.PushMessageToOutsideCB ok")
		}(data)
		return nil
	}

	c2.OnIncommingDataTransferComplete = func(data_i io.WriteSeeker) {
		imf, ok := data_i.(*goinmemfile.InMemFile)
		if !ok {
			fmt.Println("not InMemFile")
			return
		}
		fmt.Println(
			"c2.OnIncommingDataTransferComplete: got new data: ",
			string(imf.Buffer),
		)
	}

	msg := new(gojsonrpc2.Message)
	msg.Method = "functionName"
	msg.Params = map[string]any{
		"param1": 123,
		"param2": 456,
	}

	b, err := json.Marshal(msg)
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println("before c1.ChannelData()")
	imf0 := goinmemfile.NewInMemFileFromBytes(
		b,
		0,
		false,
	)
	timedout, closed, _, proto_err, err := c1.ChannelData(imf0)
	if proto_err != nil || err != nil {
		fmt.Println("proto_err:", proto_err)
		fmt.Println("err      :", err)
		return
	}
	fmt.Println("after c1.ChannelData()")

	fmt.Println(timedout)
	fmt.Println(closed)

}
