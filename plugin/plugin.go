package plugin

import (
	"fmt"
	"path"
	"strings"

	"github.com/gogo/protobuf/gogoproto"
	"github.com/gogo/protobuf/proto"
	"github.com/gogo/protobuf/protoc-gen-gogo/descriptor"
	"github.com/gogo/protobuf/protoc-gen-gogo/generator"

	worm "github.com/cjp2600/protoc-gen-worm/plugin/options"
)

type WormPlugin struct {
	*generator.Generator
	generator.PluginImports

	EmptyFiles      []string
	currentPackage  string
	currentFile     *generator.FileDescriptor
	Entities        []string
	PrivateEntities map[string]PrivateEntity
	ConvertEntities map[string]ConvertEntity
	Fields          map[string][]*descriptor.FieldDescriptorProto

	connectGlobalVar   string
	clientGlobalVar    string
	connectMethodName  string
	codecMethodName    string
	setCacheMethodName string
	getCacheMethodName string

	// build options
	Migrate   bool
	DBDriver  string
	localName string
	useTime   bool
}

type ConvertEntity struct {
	nameFrom string
	nameTo   string
	message  *generator.Descriptor
}

type PrivateEntity struct {
	name    string
	items   []*descriptor.FieldDescriptorProto
	message *generator.Descriptor
}

var ServiceName string

func NewWormPlugin(generator *generator.Generator) *WormPlugin {
	return &WormPlugin{Generator: generator}
}

func (w *WormPlugin) GetDBDriver() string {
	if len(w.DBDriver) > 0 {
		return strings.ToLower(w.DBDriver)
	}
	return "postgres"
}

func (w *WormPlugin) DBDriverImport() {
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

func (w *WormPlugin) generateModelName(name string) string {
	return name + "WORM"
}

func (w *WormPlugin) nameWithServicePrefix(funcName string) string {
	return ServiceName + funcName
}

func (w *WormPlugin) privateNameWithServicePrefix(funcName string) string {
	return strings.ToLower(ServiceName) + funcName
}

func (w *WormPlugin) privateName(funcName string) string {
	return strings.ToLower(funcName)
}

func (w *WormPlugin) Name() string {
	return "worm"
}

func (w *WormPlugin) GenerateImports(file *generator.FileDescriptor) {
	w.Generator.PrintImport("errors", "errors")
	w.Generator.PrintImport("cache", "github.com/go-redis/cache")
	w.Generator.PrintImport("redis", "github.com/go-redis/redis")
	w.Generator.PrintImport("msgpack", "github.com/vmihailenco/msgpack")
	w.Generator.PrintImport("os", "os")
	w.Generator.PrintImport("gorm", "github.com/jinzhu/gorm")
	w.Generator.PrintImport("valid", "github.com/asaskevich/govalidator")
	if w.useTime {
		w.Generator.PrintImport("time", "time")
		w.Generator.PrintImport("ptypes", "github.com/golang/protobuf/ptypes")
	}
	w.DBDriverImport()
}

func (w *WormPlugin) Init(gen *generator.Generator) {
	generator.RegisterPlugin(NewWormPlugin(gen))
	w.Generator = gen
}

func (w *WormPlugin) Generate(file *generator.FileDescriptor) {
	w.PrivateEntities = make(map[string]PrivateEntity)
	w.ConvertEntities = make(map[string]ConvertEntity)
	w.Fields = make(map[string][]*descriptor.FieldDescriptorProto)

	w.localName = generator.FileName(file)
	ServiceName = w.GetServiceName(file)
	w.generateGlobalVariables()
	w.generateRedisConnection()
	// generate structures
	for _, msg := range file.Messages() {
		name := w.generateModelName(msg.GetName())

		w.setCovertEntities(msg, name)
		w.generateModelStructures(msg, name)
		w.generateValidationMethods(msg)
		w.geterateGormMethods(msg)

		if wormMessage, ok := w.getMessageOptions(msg); ok {
			if wormMessage.GetModel() {
				w.toPB(msg)
				w.toGorm(msg)
				w.GenerateTableName(msg)
				if wormMessage.GetMigrate() {
					w.Entities = append(w.Entities, name)
				}
			}
		}
	}
	for _, msg := range file.Messages() {
		name := strings.Trim(w.generateModelName(msg.GetName()), " ")
		w.Fields[name] = msg.GetField()
		if val, ok := w.PrivateEntities[name]; ok {
			val.items = msg.GetField()
			w.PrivateEntities[name] = val
		}
	}
	// generate merge and covert methods
	w.generateEntitiesMethods()
	// generate connection methods
	w.generateConnectionMethods()
}

func (w *WormPlugin) getFieldOptions(field *descriptor.FieldDescriptorProto) *worm.WormFieldOptions {
	if field.Options == nil {
		return nil
	}
	v, err := proto.GetExtension(field.Options, worm.E_Field)
	if err != nil {
		return nil
	}
	opts, ok := v.(*worm.WormFieldOptions)
	if !ok {
		return nil
	}
	return opts
}

func (w *WormPlugin) generateValidationMethods(message *generator.Descriptor) {
	w.P(`// isValid - validation method of the described protobuf structure `)
	name := w.generateModelName(message.GetName())
	w.P(`func (e *`, name, `) IsValid() error {`)
	w.P(`if _, err := valid.ValidateStruct(e); err != nil {`)
	w.P(`return err`)
	w.P(`}`)
	w.P(`return nil`)
	w.P(`}`)
	w.Out()
	w.P(``)
}

func (w *WormPlugin) goMapTypeCustomPB(d *generator.Descriptor, field *descriptor.FieldDescriptorProto) (*generator.GoMapDescriptor, bool) {
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

func (w *WormPlugin) goMapTypeCustomGorm(d *generator.Descriptor, field *descriptor.FieldDescriptorProto) (*generator.GoMapDescriptor, bool) {
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

func (w *WormPlugin) getMessageOptions(message *generator.Descriptor) (*worm.WormMessageOptions, bool) {
	opt := message.GetOptions()
	if opt != nil {
		v, err := proto.GetExtension(opt, worm.E_Opts)
		if err != nil {
			return nil, false
		}
		wormMessage, ok := v.(*worm.WormMessageOptions)
		if !ok {
			return nil, false
		}
		return wormMessage, true
	}
	return nil, false
}

func (w *WormPlugin) GetServiceName(file *generator.FileDescriptor) string {
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
func (w *WormPlugin) generateGlobalVariables() {
	w.P(`// global gorm variable`)
	dataStoreStructure := w.nameWithServicePrefix("DB")
	w.P(`var `, dataStoreStructure, ` *gorm.DB`)
}

func (w *WormPlugin) generateConnectionMethods() {
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

func (w *WormPlugin) CreateDataStoreStructure(name string) {
	db := w.nameWithServicePrefix("DB")
	w.P()
	w.P(`// `, name, ` - data store`)
	w.P(`type `, name, ` struct {`)
	w.P(`}`)
	functionName := "New" + name

	w.P(`// `, functionName, ` - dataStore constructor`)
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

func (w *WormPlugin) setCovertEntities(message *generator.Descriptor, name string) {
	opt, ok := w.getMessageOptions(message)
	if ok {
		if entity := opt.GetConvertTo(); len(entity) > 0 {
			st := strings.Split(entity, ",")
			if len(st) > 0 {
				for _, str := range st {
					nameTo := strings.Trim(w.generateModelName(str), " ")
					w.ConvertEntities[nameTo] = ConvertEntity{
						nameFrom: name,
						nameTo:   nameTo,
						message:  message,
					}
				}
			}
		}
	}
}

func (w *WormPlugin) generateModelStructures(message *generator.Descriptor, name string) {
	w.P(`// create gorm model from protobuf (`, name, `)`)
	w.P(`type `, name, ` struct {`)

	opt, ok := w.getMessageOptions(message)
	if ok {
		if table := opt.GetMerge(); len(table) > 0 {
			st := strings.Split(table, ",")
			if len(st) > 0 {
				for _, str := range st {
					w.P(w.generateModelName(str))
					w.PrivateEntities[strings.Trim(w.generateModelName(str), " ")] = PrivateEntity{name: name, message: message}
				}
			}
		}
	}

	for _, field := range message.GetField() {
		fieldName := field.GetName()
		oneOf := field.OneofIndex != nil
		goTyp, _ := w.GoType(message, field)
		fieldName = generator.CamelCase(fieldName)
		wgromField := w.getFieldOptions(field)
		var tagString string
		if wgromField != nil && wgromField.Tag != nil {
			gormTag := wgromField.Tag.GetGorm()

			tagString = "`"
			if len(gormTag) > 0 {
				tagString = tagString + `gorm:"` + gormTag + `"`
			}

			validTag := wgromField.Tag.GetValidator()
			if len(validTag) > 0 {

				if len(gormTag) > 0 {
					tagString = tagString + " "
				}

				tagString = tagString + `valid:"` + validTag + `"`
			}
			tagString = tagString + "`"
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

	opt, ok = w.getMessageOptions(message)
	if ok {
		if model := opt.GetModel(); model {
			w.P(`gorm *gorm.DB`, " `gorm:\"-\"`")
			w.P(`cacheKey string`, " `gorm:\"-\"`")
		}
	}

	w.P(`}`)
}

func (w *WormPlugin) toPB(message *generator.Descriptor) {
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

func (w *WormPlugin) toGorm(message *generator.Descriptor) {
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

func (w *WormPlugin) ToGormFields(field *descriptor.FieldDescriptorProto, message *generator.Descriptor, bomField *worm.WormFieldOptions) {
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

func (w *WormPlugin) ToPBFields(field *descriptor.FieldDescriptorProto, message *generator.Descriptor, wGormFieldOptions *worm.WormFieldOptions) {
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

func (w *WormPlugin) GenerateTableName(msg *generator.Descriptor) {
	mName := w.generateModelName(msg.GetName())
	message, ok := w.getMessageOptions(msg)
	if ok {
		if model := message.GetModel(); model {
			tableName := strings.ToLower(msg.GetName())
			if table := message.GetTable(); len(table) > 0 && message.GetMigrate() {
				tableName = table
			}
			w.P(`func (e *`, mName, `) TableName() string {`)
			w.P(`return "`, tableName, `"`)
			w.P(`}`)
		}
	}
}

func (w *WormPlugin) generateEntitiesMethods() {
	if len(w.PrivateEntities) > 0 {
		for key, value := range w.PrivateEntities {
			w.P(``)
			w.P(`// Merge - merge private structure (`, value.name, `)`)
			w.P(`func (e *`, value.name, `) Merge`, strings.Trim(key, " "), ` (m *`, key, `) *`, value.name, ` {`)

			for _, field := range value.items {
				fieldName := field.GetName()
				fieldName = generator.CamelCase(fieldName)
				w.P(`e.`, fieldName, ` = m.`, fieldName)
			}

			w.P(`return e`)
			w.P(`}`)
			w.Out()
			w.P(``)
		}
	}
	if len(w.ConvertEntities) > 0 {
		for _, value := range w.ConvertEntities {
			w.P(``)
			w.P(`// To`, strings.Trim(value.nameTo, " "), ` - convert structure (`, value.nameFrom, ` -> `, value.nameTo, `)`)
			w.P(`func (e *`, value.nameFrom, `) To`, strings.Trim(value.nameTo, " "), ` () *`, value.nameTo, ` {`)
			w.P(`var entity `, value.nameTo)
			if fieldsFrom, ok := w.Fields[value.nameFrom]; ok {
				if fieldsTo, ok := w.Fields[value.nameTo]; ok {
					for _, field := range fieldsFrom {
						for _, f := range fieldsTo {
							if field.GetName() == f.GetName() {
								fieldName := field.GetName()
								fieldName = generator.CamelCase(fieldName)
								w.P(`entity.`, fieldName, ` = e.`, fieldName)
							}
						}
					}
				}
			}
			for _, pe := range w.PrivateEntities {
				if pe.name == value.nameTo {
					for _, f1 := range pe.items {
						if fieldsFrom, ok := w.Fields[value.nameFrom]; ok {
							for _, f2 := range fieldsFrom {
								if f1.GetName() == f2.GetName() {
									fieldName := f1.GetName()
									fieldName = generator.CamelCase(fieldName)
									w.P(`entity.`, fieldName, ` = e.`, fieldName)
								}
							}
						}
					}
				}
			}
			w.P(`return &entity`)
			w.P(`}`)
			w.Out()
			w.P(``)
		}
	}
}

func (w *WormPlugin) geterateGormMethods(msg *generator.Descriptor) {
	mName := w.generateModelName(msg.GetName())
	db := w.nameWithServicePrefix("DB")
	message, ok := w.getMessageOptions(msg)
	if ok {
		if model := message.GetModel(); model {
			w.P(`// New`, mName, ` create `, mName, ` gorm model of protobuf `, msg.GetName())
			w.P(`func New`, mName, `() *`, mName, ` {`)
			w.P(`var e `, mName, ``)
			w.P(`e.gorm = e.G()`)
			w.P(`return &e`)
			w.P(`}`)
			w.P(``)

			w.P(`// SetCacheKey cache key setter`)
			w.P(`func (e *`, mName, `) SetCacheKey(key string) *`, mName, ` {`)
			w.P(`e.cacheKey = key`)
			w.P(`return e`)
			w.P(`}`)
			w.P(``)

			w.P(`// GetCacheKey cache key getter`)
			w.P(`func (e *`, mName, `) GetCacheKey() string {`)
			w.P(`return e.cacheKey`)
			w.P(`}`)
			w.P(``)

			w.P(`// SetGorm setter custom gorm object`)
			w.P(`func (e *`, mName, `) SetGorm(db *gorm.DB) *`, mName, ` {`)
			w.P(`e.gorm = `, db, `.Table(e.TableName())`)
			w.P(`return e`)
			w.P(`}`)
			w.P(``)

			w.P(`// Gorm getter gorm object with table name`)
			w.P(`func (e *`, mName, `) G() *gorm.DB {`)
			w.P(`if e.gorm == nil {`)
			w.P(`e.gorm = `, w.nameWithServicePrefix("DB"), `.Table(e.TableName())`)
			w.P(`}`)
			w.P(`return e.gorm`)
			w.P(`}`)
			w.P(``)

			// Where
			w.P(`// Where wrapper`)
			w.P(`func (e *`, mName, `) Where(query interface{}, args ...interface{}) *`, mName, ` {`)
			w.P(`e.G().Where(query, args)`)
			w.P(`return e`)
			w.P(`}`)
			w.P(``)

			// FindOneWithCache
			w.P(`// SetGorm setter custom gorm object`)
			w.P(`func (e *`, mName, `) FindOneWithCache(key string, ttl time.Duration) (*`, mName, `, error) {`)
			w.P(`query := e.G()`)
			w.P(`if err := `, w.connectGlobalVar, `.Get(key, &e); err != nil {`)
			w.P(`if err := query.Find(&e).Error; gorm.IsRecordNotFoundError(err) {`)
			w.P(`return nil, fmt.Errorf("`, mName, ` not found")`)
			w.P(`}`)
			w.P(`err := `, w.connectGlobalVar, `.Set(&cache.Item{`)
			w.P(`Key:        key,`)
			w.P(`Object:     e,`)
			w.P(`Expiration: ttl,`)
			w.P(`})`)
			w.P(`if err != nil {`)
			w.P(`fmt.Printf("error %v", err)`)
			w.P(`}`)
			w.P(`}`)
			w.P(`return e, nil`)
			w.P(`}`)
			w.P(``)

		}
	}
}

func (w *WormPlugin) generateRedisConnection() {
	w.connectGlobalVar = w.nameWithServicePrefix("Connect")
	w.clientGlobalVar = w.nameWithServicePrefix("Client")
	w.connectMethodName = w.nameWithServicePrefix("ConnectionRedis")
	w.codecMethodName = w.nameWithServicePrefix("GetRedisCodec")
	w.setCacheMethodName = w.nameWithServicePrefix("SetCache")
	w.getCacheMethodName = w.nameWithServicePrefix("GetCache")

	w.P(`var `, w.connectGlobalVar, ` *cache.Codec`)
	w.P(`var `, w.clientGlobalVar, ` *redis.Client`)
	w.P(``)
	w.P(`// `, w.connectMethodName, ` redis connection`)
	w.P(`func `, w.connectMethodName, `() *redis.Client {`)
	w.P(`if `, w.clientGlobalVar, ` == nil {`)
	w.P(w.clientGlobalVar, ` = redis.NewClient(&redis.Options{`)
	w.P(`Addr:     os.Getenv("REDIS_HOST") + ":" + os.Getenv("REDIS_PORT"),`)
	w.P(`Password: os.Getenv("REDIS_PASSWORD"),`)
	w.P(`})`)
	w.P(`_, err := `, w.clientGlobalVar, `.Ping().Result()`)
	w.P(`if err != nil {`)
	w.P(`er := errors.New("redis connect/ping error: " + err.Error())`)
	w.P(`fmt.Printf("redis error: %v", er)`)
	w.P(`}`)
	w.P(`}`)
	w.P(`return `, w.clientGlobalVar)
	w.P(`}`)
	w.P(``)

	w.P(`// `, w.codecMethodName, ` get redis codec`)
	w.P(`func `, w.codecMethodName, `() *cache.Codec {`)
	w.P(`if `, w.connectGlobalVar, ` == nil {`)
	w.P(w.connectGlobalVar, ` = &cache.Codec{`)
	w.P(`Redis: `, w.connectMethodName, `(),`)
	w.P(`Marshal: func(v interface{}) ([]byte, error) {`)
	w.P(`	return msgpack.Marshal(v)`)
	w.P(`},`)
	w.P(`Unmarshal: func(b []byte, v interface{}) error {`)
	w.P(`	return msgpack.Unmarshal(b, v)`)
	w.P(`},`)
	w.P(`}`)
	w.P(`}`)
	w.P(`return `, w.connectGlobalVar)
	w.P(`}`)
	w.P(``)

	w.P(`// Set cache function`)
	w.P(`func `, w.setCacheMethodName, `(codec *cache.Codec, key string, ttl time.Duration, wanted interface{}) error {`)
	w.P(`err := codec.Set(&cache.Item{`)
	w.P(`Key:        key,`)
	w.P(`Object:     wanted,`)
	w.P(`Expiration: ttl,`)
	w.P(`})`)
	w.P(`return err`)
	w.P(`}`)
	w.P(``)

	w.P(`// Get cache function`)
	w.P(`func `, w.getCacheMethodName, `(codec *cache.Codec, key string, wanted interface{}) bool {`)
	w.P(`err := codec.Get(key, &wanted)`)
	w.P(`if err != nil {`)
	w.P(`return false`)
	w.P(`}`)
	w.P(`return true`)
	w.P(`}`)
	w.P(``)
}
