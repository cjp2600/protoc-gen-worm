package main

import (
	"github.com/cjp2600/rotoc-gen-wgorm/plugin"
	"github.com/gogo/protobuf/vanity/command"
)

func main() {
	wg := &plugin.WGPlugin{}
	response := command.GeneratePlugin(command.Read(), wg, ".pb.wg.go")
	command.Write(response)
}
