module github.com/AnimusPEXUS/gojsonrpc2/tests/test_channeler_01

go 1.19

require (
	github.com/AnimusPEXUS/goinmemfile v0.0.0-20230316152455-ed3f7f4cb983
	github.com/AnimusPEXUS/gojsonrpc2 v0.0.0-20230312205521-8ad988f6c28a
)

require (
	github.com/AnimusPEXUS/golockerreentrancycontext v0.0.0-20230205202617-6e6a53c419ed // indirect
	github.com/AnimusPEXUS/utils v0.0.0-20210503222024-302052ad562e // indirect
	github.com/satori/go.uuid v1.2.0 // indirect
)

replace github.com/AnimusPEXUS/gojsonrpc2 => ../..
