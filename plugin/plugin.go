package plugin

import (
	"github.com/gogo/protobuf/protoc-gen-gogo/generator"
	"path"
	"strings"
	//wgrom "github.com/cjp2600/protoc-gen-wgorm/plugin/options"
)

type WGPlugin struct {
	*generator.Generator
	generator.PluginImports

	EmptyFiles     []string
	currentPackage string
	currentFile    *generator.FileDescriptor

	// build options
	Migrate bool

	// db driver
	DBDriver string

	// global settings
	localName string
}

var ServiceName string

func NewWGPlugin(generator *generator.Generator) *WGPlugin {
	return &WGPlugin{Generator: generator}
}

func (w *WGPlugin) GetDBDriver() string {
	if len(w.DBDriver) > 0 {
		return strings.ToLower(w.DBDriver)
	}
	return "postgres"
}

func (w *WGPlugin) DBDriverImport() {
	switch w.GetDBDriver() {
	case "postgres":
		w.Generator.PrintImport("_", "github.com/jinzhu/gorm/dialects/postgres")
	case "mysql":
		w.Generator.PrintImport("_", "github.com/jinzhu/gorm/dialects/mysql")
	case "mssql":
		w.Generator.PrintImport("_", "github.com/jinzhu/gorm/dialects/mssql")
	case "sqlite":
		w.Generator.PrintImport("_", "github.com/jinzhu/gorm/dialects/sqlite")
	}
}

func (w *WGPlugin) Name() string {
	return "wgorm"
}

func (w *WGPlugin) GenerateImports(file *generator.FileDescriptor) {
	w.Generator.PrintImport("gorm", "github.com/jinzhu/gorm")
	w.DBDriverImport()
}

func (w *WGPlugin) Init(gen *generator.Generator) {
	generator.RegisterPlugin(NewWGPlugin(gen))
	w.Generator = gen
}

func (w *WGPlugin) Generate(file *generator.FileDescriptor) {
	w.localName = generator.FileName(file)
	ServiceName = w.GetServiceName(file)

	// generate connection methods
	w.GenerateConnectionMethods()
}

func (w *WGPlugin) GetServiceName(file *generator.FileDescriptor) string {
	var name string
	for _, svc := range file.Service {
		if svc != nil && svc.Name != nil {
			return *svc.Name
		}
	}
	name = *file.Name
	if ext := path.Ext(name); ext == ".proto" || ext == ".protodevel" {
		name = name[:len(name)-len(ext)]
	}
	return name
}

func (w *WGPlugin) functionNameWithServicePrefix(funcName string) string {
	return ServiceName + funcName
}

func (w *WGPlugin) GenerateConnectionMethods() {
	functionName := w.functionNameWithServicePrefix("DBConnection")

	w.P()
	w.P(`// `, functionName, ` - db connection`)
	w.P(`func `, functionName, `(host, port, name, user, password string) (*gorm.DB, error) {`)
	w.P(`connectionString := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=require",`)
	w.P(`host,`)
	w.P(`port,`)
	w.P(`user,`)
	w.P(`password,`)
	w.P(`name)`)
	w.P(`db, err := gorm.Open("`, w.GetDBDriver(), `", connectionString)`)
	w.P()
	w.P(`if err != nil {`)
	w.P(`return nil, err`)
	w.P(`}`)
	w.P(`return db, nil`)
	w.P(`}`)
}
