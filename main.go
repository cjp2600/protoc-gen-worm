package main

import (
	"github.com/cjp2600/protoc-gen-worm/plugin"
	"github.com/gogo/protobuf/vanity/command"
)

func main() {
	wg := &plugin.WormPlugin{}
	response := command.GeneratePlugin(command.Read(), wg, ".pb.worm.go")
	command.Write(response)
}
