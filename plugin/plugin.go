package plugin

import (
	"fmt"
	"path"
	"strings"

	"github.com/gogo/protobuf/gogoproto"
	"github.com/gogo/protobuf/proto"
	"github.com/gogo/protobuf/protoc-gen-gogo/descriptor"
	"github.com/gogo/protobuf/protoc-gen-gogo/generator"
	"github.com/serenize/snaker"

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
	JsonBFields     map[string]JsonBField

	clientGlobalVar   string
	connectMethodName string

	// build options
	Migrate   bool
	DBDriver  string
	SSLMode   bool
	localName string
	useTime   bool
	useJsonb  bool
	useUnsafe bool
}

type JsonBField struct {
	name string
	tps  string
	fld  *descriptor.FieldDescriptorProto
}

type ConvertEntity struct {
	nameFrom string
	nameTo   string
	isOneOf  bool
	message  *generator.Descriptor
}

type PrivateEntity struct {
	name    string
	isOneOf bool
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
	w.Generator.PrintImport("redis", "github.com/go-redis/redis")
	w.Generator.PrintImport("os", "os")
	w.Generator.PrintImport("gorm", "github.com/jinzhu/gorm")
	w.Generator.PrintImport("valid", "github.com/asaskevich/govalidator")
	if w.useTime {
		w.Generator.PrintImport("time", "time")
		w.Generator.PrintImport("ptypes", "github.com/golang/protobuf/ptypes")
	}
	if w.useJsonb {
		w.Generator.PrintImport("json", "encoding/json")
		w.Generator.PrintImport("postgres", "github.com/jinzhu/gorm/dialects/postgres")
	}
	if w.useUnsafe {
		w.Generator.PrintImport("unsafe", "unsafe")
	}
	w.DBDriverImport()
}

func (w *WormPlugin) Init(gen *generator.Generator) {
	generator.RegisterPlugin(NewWormPlugin(gen))
	w.Generator = gen

	if val, ok := gen.Param["SSLMode"]; ok {
		if val == "true" {
			w.SSLMode = true
		}
	}

	if val, ok := gen.Param["DBDriver"]; ok {
		w.DBDriver = val
	}
}

func (w *WormPlugin) Generate(file *generator.FileDescriptor) {
	w.PrivateEntities = make(map[string]PrivateEntity)
	w.ConvertEntities = make(map[string]ConvertEntity)
	w.JsonBFields = make(map[string]JsonBField)
	w.Fields = make(map[string][]*descriptor.FieldDescriptorProto)

	w.localName = generator.FileName(file)
	ServiceName = w.GetServiceName(file)
	w.generateGlobalVariables()
	w.generateRedisConnection()
	// generate structures
	for _, msg := range file.Messages() {
		name := w.generateModelName(msg.GetName())

		w.setJsonBFields(file)
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

	for _, msg := range file.Messages() {
		if wormMessage, ok := w.getMessageOptions(msg); ok {
			if wormMessage.GetModel() {
				w.generateUpdateMethod(msg, wormMessage.GetMerge())
			}
		}
	}

	// generate merge and covert methods
	w.generateEntitiesMethods()
	// generate connection methods
	w.generateConnectionMethods()
}

func (w *WormPlugin) setJsonBFields(file *generator.FileDescriptor) {
	for _, msg := range file.Messages() {
		name := w.generateModelName(msg.GetName())
		for _, fld := range msg.GetField() {
			fieldName := fld.GetName()
			fieldName = generator.CamelCase(fieldName)

			objectFld := w.getFieldOptions(fld)
			if objectFld != nil && objectFld.Tag != nil {
				if objectFld.Tag.GetJsonb() {
					w.JsonBFields[name+fieldName] = JsonBField{
						name: fieldName,
						tps:  fld.Type.String(),
						fld:  fld,
					}
				}
			}
		}
	}
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

func (w *WormPlugin) generateUpdateMethod(message *generator.Descriptor, privateName string) {
	name := w.generateModelName(message.GetName())

	w.P(`// Update - update model method, a check is made on existing fields.`)
	w.P(`func (e *`, name, `) UpdateIfExist(updateAt bool) (*`, name, `, error) {`)
	w.P(`updateEntities := make(map[string]interface{})`)
	w.P(`query := e.G()`)
	w.P()

	fields := message.GetField()

	if len(w.PrivateEntities) > 0 && len(privateName) > 0 {
		if val, ok := w.PrivateEntities[w.generateModelName(privateName)]; ok {
			for _, f1 := range val.items {
				fields = append(fields, f1)
			}
		}
	}

	for _, field := range fields {
		var isJsonb bool
		fieldName := field.GetName()

		wgromField := w.getFieldOptions(field)
		if wgromField != nil && wgromField.Tag != nil {
			isJsonb = wgromField.Tag.GetJsonb()
		}

		if strings.ToLower(fieldName) == "id" {
			w.P(`// check if fill id field`)
			w.P(`if len(e.Id) > 0 {`)
			w.P(`query = query.Where("id = ?", e.Id)`)
			w.P(`}`)
		}

		// skip _id field UpdatedAt
		if strings.ToLower(fieldName) == "id" || strings.ToLower(fieldName) == "createdat" || strings.ToLower(fieldName) == "updatedat" {
			continue
		}

		// find goType
		goTyp, _ := w.GoType(message, field)
		fieldName = generator.CamelCase(fieldName)
		snakeName := snaker.CamelToSnake(fieldName)
		oneOf := field.OneofIndex != nil

		if oneOf {

			w.P(`// set `, fieldName)
			w.P(`if e.`, fieldName, ` != nil {`)
			w.P(`updateEntities["`, snakeName, `"]  = e.Get`, fieldName, `()`)
			w.P(`}`)

		} else if field.IsScalar() {

			if strings.ToLower(goTyp) == "bool" {
				w.P(`// set `, fieldName)
				w.P(`if e.`, fieldName, ` {`)
				w.P(`updateEntities["`, snakeName, `"]  = e.`, fieldName)
				w.P(`}`)
			} else {
				w.P(`// set `, fieldName)
				w.P(`if e.`, fieldName, ` > 0 {`)
				w.P(`updateEntities["`, snakeName, `"]  = e.`, fieldName)
				w.P(`}`)
			}

		} else if isJsonb {

			w.P(`// set `, fieldName)
			w.P(fieldName, `bts, err := e.`, fieldName, `.RawMessage.MarshalJSON()`)
			w.P(`if err == nil {`)
			w.P(`if len(string(`, fieldName, `bts)) > 0 && string(`, fieldName, `bts) != "{}" {`)
			w.P(`updateEntities["`, snakeName, `"] = `, fieldName, `bts`)
			w.P(`}`)
			w.P(`}`)

		} else if strings.ToLower(goTyp) == "*timestamp.timestamp" {
			goTyp = "time.Time"
			w.useTime = true

			w.P(`// set `, fieldName)
			w.P(`if !e.`, fieldName, `.IsZero() {`)
			w.P(`updateEntities["`, snakeName, `"]  = e.`, fieldName)
			w.P(`}`)

		} else if w.IsMap(field) {

			w.P(`// set `, fieldName)
			w.P(`if len(e.`, fieldName, `) > 0 {`)
			w.P(`updateEntities["`, snakeName, `"]  = e.`, fieldName)
			w.P(`}`)

		} else {

			if field.IsMessage() {

				w.P(`// set `, fieldName)
				w.P(`if e.`, fieldName, ` != nil {`)
				w.P(`updateEntities["`, snakeName, `"]  = e.`, fieldName)
				w.P(`}`)

			} else {

				if field.IsEnum() {
					w.P(`// set `, fieldName)
					w.P(`if len(e.`, fieldName, `.String()) > 0 {`)
					w.P(`updateEntities["`, snakeName, `"]  = e.`, fieldName)
					w.P(`}`)
				} else {
					w.P(`// set `, fieldName)
					w.P(`if len(e.`, fieldName, `) > 0 {`)
					w.P(`updateEntities["`, snakeName, `"]  = e.`, fieldName)
					w.P(`}`)
				}
			}

		}

	}

	w.P(` if updateAt {`)
	w.P(`updateEntities["updated_at"] = time.Now()`)
	w.P(` }`)

	w.P(` if err := query.Updates(updateEntities).Error; err != nil {`)
	w.P(` return e, err`)
	w.P(` }`)

	w.P(` return e, nil`)
	w.P(`}`)
	w.P()
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

	ssl := "disable"
	if w.SSLMode {
		ssl = "require"
	}

	w.P()
	w.P(`// `, functionName, ` - db connection`)
	w.P(`func (d *`, dataStoreStructure, `) `, functionName, `(host, port, name, user, password string) (*gorm.DB, error) {`)
	w.P(`var ssl string`)
	w.P(`ssl = "`, ssl, `"`)
	w.P(`if len(os.Getenv("DB_SSL_MODE")) > 0 {`)
	w.P(`ssl = os.Getenv("DB_SSL_MODE")`)
	w.P(`}`)

	w.P(`connectionString := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=" + ssl,`)
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
					w.ConvertEntities[name+":"+nameTo] = ConvertEntity{
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

	type useUnsafeMethod struct {
		fieldName string
		goTyp     string
		mName     string
	}

	var nsafeScope []useUnsafeMethod
	for _, field := range message.GetField() {
		fieldName := field.GetName()
		oneOf := field.OneofIndex != nil
		goTyp, _ := w.GoType(message, field)
		var isJsonb bool

		if oneOf && strings.ToLower(goTyp) == "*timestamp.timestamp" {
			goTyp = "time.Time"
		}
		if oneOf {
			w.useUnsafe = true
			nsafeScope = append(nsafeScope, useUnsafeMethod{
				fieldName: strings.Title(fieldName),
				goTyp:     goTyp,
				mName:     name,
			})
		}

		fieldName = generator.CamelCase(fieldName)
		wgromField := w.getFieldOptions(field)
		var tagString string
		if wgromField != nil && wgromField.Tag != nil {
			gormTag := wgromField.Tag.GetGorm()
			isJsonb = wgromField.Tag.GetJsonb()

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

			if strings.ToLower(goTyp) == "*timestamp.timestamp" {
				w.P(fieldName, ` *time.Time`, tagString)
				w.useTime = true
			} else {
				w.P(fieldName, ` *`, goTyp, tagString)
			}

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
		} else if isJsonb {
			w.useJsonb = true
			w.P(fieldName, ` `, `postgres.Jsonb`, tagString)
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

	for _, s := range nsafeScope {
		w.P(``)
		w.P(`//Check method `, s.fieldName, ` - update field`)
		w.P(`func (e *`, name, `) Get`, s.fieldName, `() `, s.goTyp, ` {`)
		w.P(`var resp `, s.goTyp)
		w.P(`if e.`, s.fieldName, ` != nil {`)
		w.P(`resp = *((*`, s.goTyp, `)(unsafe.Pointer(e.`, s.fieldName, `)))`)
		w.P(`}`)
		w.P(`return resp`)
		w.P(`}`)
		w.P(``)
	}
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

	name := w.generateModelName(message.GetName())
	fieldName := field.GetName()
	fieldName = generator.CamelCase(fieldName)
	goTyp, _ := w.GoType(message, field)
	oneof := field.OneofIndex != nil

	var jField *JsonBField
	if val, ok := w.JsonBFields[name+fieldName]; ok {
		jField = &val
	}

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
	} else if jField != nil {

		if field.IsRepeated() && jField.tps == "TYPE_STRING" {

			w.P(`// convert to Gorm object json message`)
			w.P(fieldName, `json, err := json.Marshal(e.`, fieldName, `)`)
			w.P(`if err == nil {`)
			w.P(`resp.`, fieldName, ` =  postgres.Jsonb{json.RawMessage(`, fieldName, `json)}`)
			w.P(`}`)

		} else {
			w.P(`// convert to Gorm object json message`)
			w.P(`resp.`, fieldName, ` =  postgres.Jsonb{json.RawMessage(e.`, fieldName, `)}`)
		}

	} else {
		if oneof {
			sourceName := w.GetFieldName(message, field)
			w.P(`// oneof link`)
			w.P(`if e.Get`, sourceName, `() != nil {`)
			w.P(`value :=  e.Get`, fieldName, `()`)
			w.P(`resp.`, fieldName, ` = &value`)
			w.P(`}`)
			w.P(``)
		} else {
			w.P(`resp.`, fieldName, ` = e.`, fieldName)
		}
	}
	w.Out()
}

func (w *WormPlugin) ToPBFields(field *descriptor.FieldDescriptorProto, message *generator.Descriptor, wGormFieldOptions *worm.WormFieldOptions) {

	name := w.generateModelName(message.GetName())
	fieldName := field.GetName()
	fieldName = generator.CamelCase(fieldName)
	oneof := field.OneofIndex != nil
	goTyp, _ := w.GoType(message, field)
	w.In()

	var jField *JsonBField
	if val, ok := w.JsonBFields[name+fieldName]; ok {
		jField = &val
	}

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
	} else if jField != nil {

		if field.IsRepeated() && jField.tps == "TYPE_STRING" {

			w.P(`// convert jsonb to string`)
			w.P(`var `, fieldName, `Str []string`)
			w.P(`if err := json.Unmarshal([]byte(e.`, fieldName, `.RawMessage), &`, fieldName, `Str); err != nil {`)
			w.P(`fmt.Println(err)`)
			w.P(`} else {`)
			w.P(`resp.`, fieldName, ` = `, fieldName, `Str`)
			w.P(`}`)

		} else {
			w.P(`// convert jsonb to string`)
			w.P(fieldName, `JsonbString, _ :=  e.`, fieldName, `.MarshalJSON()`)
			w.P(`resp.`, fieldName, ` = string(`, fieldName, `JsonbString)`)
		}

	} else {
		if oneof {
			sourceName := w.GetFieldName(message, field)
			interfaceName := w.Generator.OneOfTypeName(message, field)
			w.P(`resp.`, sourceName, ` = &`, interfaceName, `{e.Get`, fieldName, `()}`)
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
						var isJsonb bool
						for _, f := range fieldsTo {
							if field.GetName() == f.GetName() {

								wgromField := w.getFieldOptions(f)
								if wgromField != nil && wgromField.Tag != nil {
									isJsonb = wgromField.Tag.GetJsonb()
								}
								if isJsonb {

									fieldName := field.GetName()
									fieldName = generator.CamelCase(fieldName)

									w.P(`// convert jsonb from []`)
									w.P(fieldName, `JsonbBytes, _ := json.Marshal(e.`, fieldName, `)`)
									w.P(`entity.`, fieldName, ` = postgres.Jsonb{json.RawMessage(`, fieldName, `JsonbBytes)}`)

									continue
								}

								// skip if not equal oneOf
								oneoField := field.OneofIndex != nil
								oneoF := f.OneofIndex != nil
								if oneoF {
									if !oneoField {
										continue
									}
								}

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

		}
	}
}

func (w *WormPlugin) generateRedisConnection() {
	w.clientGlobalVar = w.nameWithServicePrefix("RedisClient")
	w.connectMethodName = w.nameWithServicePrefix("ConnectionRedis")

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

}
