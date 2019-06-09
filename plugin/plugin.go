package plugin

import (
	"fmt"
	wgrom "github.com/cjp2600/protoc-gen-wgorm/plugin/options"
	"github.com/gogo/protobuf/gogoproto"
	"github.com/gogo/protobuf/proto"
	"github.com/gogo/protobuf/protoc-gen-gogo/descriptor"
	"github.com/gogo/protobuf/protoc-gen-gogo/generator"
	"path"
	"strings"
)

type WGPlugin struct {
	*generator.Generator
	generator.PluginImports

	EmptyFiles     []string
	currentPackage string
	currentFile    *generator.FileDescriptor
	Entities       []string

	// build options
	Migrate bool

	// db driver
	DBDriver string

	// global settings
	localName string

	// imports
	useTime bool
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

func (w *WGPlugin) generateModelName(name string) string {
	return name + "Gorm"
}

func (w *WGPlugin) nameWithServicePrefix(funcName string) string {
	return ServiceName + funcName
}

func (w *WGPlugin) privateNameWithServicePrefix(funcName string) string {
	return strings.ToLower(ServiceName) + funcName
}

func (w *WGPlugin) privateName(funcName string) string {
	return strings.ToLower(funcName)
}

func (w *WGPlugin) Name() string {
	return "wgorm"
}

func (w *WGPlugin) GenerateImports(file *generator.FileDescriptor) {
	w.Generator.PrintImport("os", "os")
	w.Generator.PrintImport("gorm", "github.com/jinzhu/gorm")
	if w.useTime {
		w.Generator.PrintImport("time", "time")
		w.Generator.PrintImport("ptypes", "github.com/golang/protobuf/ptypes")
	}
	w.DBDriverImport()
}

func (w *WGPlugin) Init(gen *generator.Generator) {
	generator.RegisterPlugin(NewWGPlugin(gen))
	w.Generator = gen
}

func (w *WGPlugin) Generate(file *generator.FileDescriptor) {
	w.localName = generator.FileName(file)
	ServiceName = w.GetServiceName(file)
	w.generateGlobalVariables()
	// generate structures
	for _, msg := range file.Messages() {
		if wgormMessage, ok := w.getMessageOptions(msg); ok {
			name := w.generateModelName(msg.GetName())
			w.toPB(msg)
			w.toGorm(msg)
			w.GenerateTableName(msg)
			if wgormMessage.GetModel() {
				w.generateModelStructures(msg, name)
			}
			if wgormMessage.GetMigrate() {
				w.Entities = append(w.Entities, name)
			}
		}
	}
	// generate connection methods
	w.generateConnectionMethods()
}

func (w *WGPlugin) getFieldOptions(field *descriptor.FieldDescriptorProto) *wgrom.WGormFieldOptions {
	if field.Options == nil {
		return nil
	}
	v, err := proto.GetExtension(field.Options, wgrom.E_Field)
	if err != nil {
		return nil
	}
	opts, ok := v.(*wgrom.WGormFieldOptions)
	if !ok {
		return nil
	}
	return opts
}

func (w *WGPlugin) goMapTypeCustomPB(d *generator.Descriptor, field *descriptor.FieldDescriptorProto) (*generator.GoMapDescriptor, bool) {
	var isMessage = false
	if d == nil {
		byName := w.ObjectNamed(field.GetTypeName())
		desc, ok := byName.(*generator.Descriptor)
		if byName == nil || !ok || !desc.GetOptions().GetMapEntry() {
			w.Fail(fmt.Sprintf("field %s is not a map", field.GetTypeName()))
			return nil, false
		}
		d = desc
	}

	m := &generator.GoMapDescriptor{
		KeyField:   d.Field[0],
		ValueField: d.Field[1],
	}

	// Figure out the Go types and tags for the key and value types.
	m.KeyAliasField, m.ValueAliasField = w.GetMapKeyField(field, m.KeyField), w.GetMapValueField(field, m.ValueField)
	keyType, _ := w.GoType(d, m.KeyAliasField)
	valType, _ := w.GoType(d, m.ValueAliasField)

	// We don't use stars, except for message-typed values.
	// Message and enum types are the only two possibly foreign types used in maps,
	// so record their use. They are not permitted as map keys.
	keyType = strings.TrimPrefix(keyType, "*")
	switch *m.ValueAliasField.Type {
	case descriptor.FieldDescriptorProto_TYPE_ENUM:
		valType = strings.TrimPrefix(valType, "*")
		w.RecordTypeUse(m.ValueAliasField.GetTypeName())
	case descriptor.FieldDescriptorProto_TYPE_MESSAGE:
		if !gogoproto.IsNullable(m.ValueAliasField) {
			valType = strings.TrimPrefix(valType, "*")
		}
		if !gogoproto.IsStdType(m.ValueAliasField) && !gogoproto.IsCustomType(field) && !gogoproto.IsCastType(field) {
			isMessage = true
			w.RecordTypeUse(m.ValueAliasField.GetTypeName())
		}
	default:
		if gogoproto.IsCustomType(m.ValueAliasField) {
			if !gogoproto.IsNullable(m.ValueAliasField) {

				valType = strings.TrimPrefix(valType, "*")
			}
			if !gogoproto.IsStdType(field) {
				w.RecordTypeUse(m.ValueAliasField.GetTypeName())
			}
		} else {

			valType = strings.TrimPrefix(valType, "*")
		}
	}

	m.GoType = fmt.Sprintf("map[%s]%s", keyType, valType)
	return m, isMessage
}

func (w *WGPlugin) goMapTypeCustomGorm(d *generator.Descriptor, field *descriptor.FieldDescriptorProto) (*generator.GoMapDescriptor, bool) {
	var isMessage = false
	if d == nil {
		byName := w.ObjectNamed(field.GetTypeName())
		desc, ok := byName.(*generator.Descriptor)
		if byName == nil || !ok || !desc.GetOptions().GetMapEntry() {
			w.Fail(fmt.Sprintf("field %s is not a map", field.GetTypeName()))
			return nil, false
		}
		d = desc
	}

	m := &generator.GoMapDescriptor{
		KeyField:   d.Field[0],
		ValueField: d.Field[1],
	}

	// Figure out the Go types and tags for the key and value types.
	m.KeyAliasField, m.ValueAliasField = w.GetMapKeyField(field, m.KeyField), w.GetMapValueField(field, m.ValueField)
	keyType, _ := w.GoType(d, m.KeyAliasField)
	valType, _ := w.GoType(d, m.ValueAliasField)

	// We don't use stars, except for message-typed values.
	// Message and enum types are the only two possibly foreign types used in maps,
	// so record their use. They are not permitted as map keys.
	keyType = strings.TrimPrefix(keyType, "*")
	switch *m.ValueAliasField.Type {
	case descriptor.FieldDescriptorProto_TYPE_ENUM:
		valType = strings.TrimPrefix(valType, "*")
		w.RecordTypeUse(m.ValueAliasField.GetTypeName())
	case descriptor.FieldDescriptorProto_TYPE_MESSAGE:
		if !gogoproto.IsNullable(m.ValueAliasField) {
			valType = strings.TrimPrefix(valType, "*")
		}
		if !gogoproto.IsStdType(m.ValueAliasField) && !gogoproto.IsCustomType(field) && !gogoproto.IsCastType(field) {
			valType = w.generateModelName(valType)
			isMessage = true
			w.RecordTypeUse(m.ValueAliasField.GetTypeName())
		}
	default:
		if gogoproto.IsCustomType(m.ValueAliasField) {
			if !gogoproto.IsNullable(m.ValueAliasField) {

				valType = strings.TrimPrefix(valType, "*")
			}
			if !gogoproto.IsStdType(field) {
				w.RecordTypeUse(m.ValueAliasField.GetTypeName())
			}
		} else {

			valType = strings.TrimPrefix(valType, "*")
		}
	}

	m.GoType = fmt.Sprintf("map[%s]%s", keyType, valType)
	return m, isMessage
}

func (w *WGPlugin) getMessageOptions(message *generator.Descriptor) (*wgrom.WGormMessageOptions, bool) {
	opt := message.GetOptions()
	if opt != nil {
		v, err := proto.GetExtension(opt, wgrom.E_Opts)
		if err != nil {
			return nil, false
		}
		wgormMessage, ok := v.(*wgrom.WGormMessageOptions)
		if !ok {
			return nil, false
		}
		return wgormMessage, true
	}
	return nil, false
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
func (w *WGPlugin) generateGlobalVariables() {
	w.P(`// global gorm variable`)
	dataStoreStructure := w.nameWithServicePrefix("DB")
	w.P(`var `, dataStoreStructure, ` *gorm.DB`)
}

func (w *WGPlugin) generateConnectionMethods() {
	dataStoreStructure := w.nameWithServicePrefix("DataStore")
	functionName := w.privateName("Connection")
	// create dataStore
	w.CreateDataStoreStructure(dataStoreStructure)

	w.P()
	w.P(`// `, functionName, ` - db connection`)
	w.P(`func (d *`, dataStoreStructure, `) `, functionName, `(host, port, name, user, password string) (*gorm.DB, error) {`)
	w.P(`connectionString := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=require",`)
	w.P(`host,`)
	w.P(`port,`)
	w.P(`user,`)
	w.P(`password,`)
	w.P(`name)`)
	w.P(`db, err := gorm.Open("`, w.GetDBDriver(), `", connectionString)`)
	w.P(`if err != nil {`)
	w.P(`return nil, err`)
	w.P(`}`)
	w.P(`return db, nil`)
	w.P(`}`)
}

func (w *WGPlugin) CreateDataStoreStructure(name string) {
	db := w.nameWithServicePrefix("DB")
	w.P()
	w.P(`// `, name, ` - data store`)
	w.P(`type `, name, ` struct {`)
	w.P(`}`)
	functionName := "New" + name
	w.P(`// `, functionName, ` - datastore constructor`)
	w.P(`func `, functionName, `(logging bool, maxConnection int) (*`, name, `, error) {`)
	w.P(`store := &`, name, `{}`)
	w.P(`db, err := store.connection(os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_NAME"), os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD"))`)
	w.P(`if err != nil {`)
	w.P(`return store, err`)
	w.P(`}`)
	w.P(`if maxConnection > 0 {`)
	w.P(`db.DB().SetMaxIdleConns(maxConnection)`)
	w.P(`}`)
	w.P(`db.LogMode(logging)`)
	w.P()
	w.P(`if `, db, ` == nil {`)
	w.P(db, ` = db`)
	w.P(`}`)
	w.P()
	w.P(`store.migrate()`)
	w.P(`return store, err`)
	w.P(`}`)
	w.P()
	w.P(`// Migrate - gorm AutoMigrate`)
	w.P(`func (d *`, name, `) migrate() {`)
	if len(w.Entities) > 0 {
		w.P(db, `.AutoMigrate(`)
		for _, enitity := range w.Entities {
			w.P(`&`, enitity, `{},`)
		}
		w.P(`)`)
	}
	w.P(`}`)
}

func (w *WGPlugin) generateModelStructures(message *generator.Descriptor, name string) {
	w.P(`// create gorm model from protobuf (`, name, `)`)
	w.P(`type `, name, ` struct {`)
	for _, field := range message.GetField() {
		fieldName := field.GetName()
		oneOf := field.OneofIndex != nil
		goTyp, _ := w.GoType(message, field)
		fieldName = generator.CamelCase(fieldName)
		wgromField := w.getFieldOptions(field)
		var tagString string
		if wgromField != nil && wgromField.Tag != nil {
			gormTag := wgromField.Tag.GetGorm()
			if len(gormTag) > 0 {
				tagString = "`"
				tagString = tagString + `gorm:"` + gormTag + `"`
				tagString = tagString + "`"
			}
		}
		if oneOf {
			w.P(fieldName, ` `, goTyp, tagString)
		} else if w.IsMap(field) {
			m, _ := w.goMapTypeCustomGorm(nil, field)
			w.P(fieldName, ` `, m.GoType, tagString)
		} else if (field.IsMessage() && !gogoproto.IsCustomType(field) && !gogoproto.IsStdType(field)) || w.IsGroup(field) {
			if strings.ToLower(goTyp) == "*timestamp.timestamp" {
				w.P(fieldName, ` time.Time`, tagString)
				w.useTime = true
			} else {
				w.P(fieldName, ` `, w.generateModelName(goTyp), tagString)
			}
		} else {
			w.P(fieldName, ` `, goTyp, tagString)
		}
	}
	w.P(`}`)
}

func (w *WGPlugin) toPB(message *generator.Descriptor) {
	w.In()
	mName := w.generateModelName(message.GetName())
	w.P(`func (e *`, mName, `) ToPB() *`, message.GetName(), ` {`)
	w.P(`var resp `, message.GetName())
	for _, field := range message.GetField() {
		bomField := w.getFieldOptions(field)
		w.ToPBFields(field, message, bomField)
	}
	w.P(`return &resp`)
	w.P(`}`)
	w.Out()
	w.P(``)
}

func (w *WGPlugin) toGorm(message *generator.Descriptor) {
	w.In()
	mName := w.generateModelName(message.GetName())
	w.P(`func (e *`, message.GetName(), `) ToGorm() *`, mName, ` {`)
	w.P(`var resp `, mName)
	for _, field := range message.GetField() {
		bomwgromFieldsield := w.getFieldOptions(field)
		w.ToGormFields(field, message, bomwgromFieldsield)
	}
	w.P(`return &resp`)
	w.P(`}`)
	w.Out()
	w.P(``)
}

func (w *WGPlugin) ToGormFields(field *descriptor.FieldDescriptorProto, message *generator.Descriptor, bomField *wgrom.WGormFieldOptions) {
	fieldName := field.GetName()
	fieldName = generator.CamelCase(fieldName)
	goTyp, _ := w.GoType(message, field)
	oneof := field.OneofIndex != nil
	w.In()
	if w.IsMap(field) {
		m, ism := w.goMapTypeCustomGorm(nil, field)
		_, keyField, keyAliasField := m.GoType, m.KeyField, m.KeyAliasField
		keygoTyp, _ := w.GoType(nil, keyField)
		keygoTyp = strings.Replace(keygoTyp, "*", "", 1)
		keygoAliasTyp, _ := w.GoType(nil, keyAliasField)
		keygoAliasTyp = strings.Replace(keygoAliasTyp, "*", "", 1)
		w.P(`tt`, fieldName, ` := make(`, m.GoType, `)`)
		w.P(`for k, v := range e.`, fieldName, ` {`)
		w.In()
		if ism {
			w.P(`tt`, fieldName, `[k] = v.ToGorm()`)
		} else {
			w.P(`tt`, fieldName, `[k] = v`)
		}
		w.Out()
		w.P(`}`)
		w.P(`resp.`, fieldName, ` = tt`, fieldName)
	} else if (field.IsMessage() && !gogoproto.IsCustomType(field) && !gogoproto.IsStdType(field)) || w.IsGroup(field) {
		if strings.ToLower(goTyp) == "*timestamp.timestamp" {
			w.useTime = true
			w.P(`// create time object`)
			w.P(`ut`, fieldName, ` := time.Unix(e.`, fieldName, `.GetSeconds(), int64(e.`, fieldName, `.GetNanos()))`)
			w.P(`resp.`, fieldName, ` = ut`, fieldName)
		} else if field.IsMessage() {
			repeated := field.IsRepeated()
			if repeated {
				w.P(`// create nested mongo`)
				w.P(`var sub`, fieldName, w.generateModelName(goTyp))
				w.P(`if e.`, fieldName, ` != nil {`)
				w.P(`if len(e.`, fieldName, `) > 0 {`)
				w.P(`for _, b := range `, `e.`, fieldName, `{`)
				w.P(`if b != nil {`)
				w.P(`sub`, fieldName, ` = append(sub`, fieldName, `, b.ToGorm())`)
				w.P(`}`)
				w.P(`}`)
				w.P(`}`)
				w.P(`}`)
				w.P(`resp.`, fieldName, ` = sub`, fieldName)
			} else {
				w.P(`// create single mongo`)
				w.P(`if e.`, fieldName, ` != nil {`)
				w.P(`resp.`, fieldName, ` = e.`, fieldName, `.ToGorm()`)
				w.P(`}`)
			}
		} else {
			w.P(`resp.`, fieldName, ` = e.`, fieldName)
		}
	} else {
		if oneof {
			sourceName := w.GetFieldName(message, field)
			w.P(`// oneof link`)
			w.P(`if e.Get`, sourceName, `() != nil {`)
			w.P(`resp.`, fieldName, ` = e.Get`, fieldName, `()`)
			w.P(`}`)
			w.P(``)
		} else {
			w.P(`resp.`, fieldName, ` = e.`, fieldName)
		}
	}
	w.Out()
}

func (w *WGPlugin) ToPBFields(field *descriptor.FieldDescriptorProto, message *generator.Descriptor, wGormFieldOptions *wgrom.WGormFieldOptions) {
	fieldName := field.GetName()
	fieldName = generator.CamelCase(fieldName)
	oneof := field.OneofIndex != nil
	goTyp, _ := w.GoType(message, field)
	w.In()
	if w.IsMap(field) {
		m, ism := w.goMapTypeCustomPB(nil, field)
		_, keyField, keyAliasField := m.GoType, m.KeyField, m.KeyAliasField
		keygoTyp, _ := w.GoType(nil, keyField)
		keygoTyp = strings.Replace(keygoTyp, "*", "", 1)
		keygoAliasTyp, _ := w.GoType(nil, keyAliasField)
		keygoAliasTyp = strings.Replace(keygoAliasTyp, "*", "", 1)
		w.P(`tt`, fieldName, ` := make(`, m.GoType, `)`)
		w.P(`for k, v := range e.`, fieldName, ` {`)
		w.In()
		if ism {
			w.P(`tt`, fieldName, `[k] = v.ToPB()`)
		} else {
			w.P(`tt`, fieldName, `[k] = v`)
		}
		w.Out()
		w.P(`}`)
		w.P(`resp.`, fieldName, ` = tt`, fieldName)
	} else if (field.IsMessage() && !gogoproto.IsCustomType(field) && !gogoproto.IsStdType(field)) || w.IsGroup(field) {
		if strings.ToLower(goTyp) == "*timestamp.timestamp" {
			w.P(`ptap`, fieldName, `, _ := ptypes.TimestampProto(e.`, fieldName, `)`)
			w.useTime = true
			w.P(`resp.`, fieldName, ` = ptap`, fieldName)
		} else if field.IsMessage() {
			repeated := field.IsRepeated()
			if repeated {
				w.P(`// create nested pb`)
				w.P(`var sub`, fieldName, goTyp)
				w.P(`if e.`, fieldName, ` != nil {`)
				w.P(`if len(e.`, fieldName, `) > 0 {`)
				w.P(`for _, b := range `, `e.`, fieldName, `{`)
				w.P(`sub`, fieldName, ` = append(sub`, fieldName, `, b.ToPB())`)
				w.P(`}`)
				w.P(`}`)
				w.P(`}`)
				w.P(`resp.`, fieldName, ` = sub`, fieldName)
			} else {
				w.P(`// create single pb`)
				w.P(`if e.`, fieldName, ` != nil {`)
				w.P(`resp.`, fieldName, ` = e.`, fieldName, `.ToPB()`)
				w.P(`}`)
			}
		} else {
			w.P(`resp.`, fieldName, ` = e.`, fieldName)
		}
	} else {
		if oneof {
			sourceName := w.GetFieldName(message, field)
			interfaceName := w.Generator.OneOfTypeName(message, field)
			w.P(`resp.`, sourceName, ` = &`, interfaceName, `{e.`, fieldName, `}`)
		} else {
			w.P(`resp.`, fieldName, ` = e.`, fieldName)
		}
	}
	w.Out()
}

func (w *WGPlugin) GenerateTableName(msg *generator.Descriptor) {
	var tableName string
	mName := w.generateModelName(msg.GetName())
	w.P(`func (`, mName, `) TableName() string {`)
	message, ok := w.getMessageOptions(msg)
	if ok {
		tableName = strings.ToLower(msg.GetName())
		if table := message.GetTable(); len(table) > 0 {
			tableName = table
		}
	}
	w.P(`return "`, tableName, `"`)
	w.P(`}`)
}
