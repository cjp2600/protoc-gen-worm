package main

import (
	"github.com/cjp2600/protoc-gen-wgorm/plugin"
	"github.com/gogo/protobuf/vanity/command"
)

func main() {
	wg := &plugin.WGPlugin{}
	response := command.GeneratePlugin(command.Read(), wg, ".pb.wgorm.go")
	command.Write(response)
}
