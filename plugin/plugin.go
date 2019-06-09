package plugin

import (
	"github.com/gogo/protobuf/protoc-gen-gogo/generator"
)

type WGPlugin struct {
	*generator.Generator
	generator.PluginImports

	EmptyFiles     []string
	currentPackage string
	currentFile    *generator.FileDescriptor
	generateCrud   bool

	usePrimitive bool
	useTime      bool
	useStrconv   bool
	useCrud      bool
	useUnsafe    bool

	localName string
}

var ServiceName string

func NewWGPlugin(generator *generator.Generator) *WGPlugin {
	return &WGPlugin{Generator: generator}
}

func (w *WGPlugin) Name() string {
	panic("implement me")
}

func (w *WGPlugin) Init(g *generator.Generator) {
	generator.RegisterPlugin(NewWGPlugin(g))
	w.Generator = g
}

func (w *WGPlugin) Generate(file *generator.FileDescriptor) {
	panic("implement me")
}
