package binder

import (
	"fmt"
	"os"
	"reflect"
	"strconv"

	"github.com/mitchellh/mapstructure"
	"github.com/pkg/errors"
)

var (
	registered                       = map[string]*item{}
	RegisterCmdArgs   RegisterFunc   = defaultRegisterCmdArgsFlagStd
	mappedConf                       = MappedConfiguration{}
	mappedConfUpdater                = map[string]*funcUpdater{}
	LoadConfig        LoadConfigFunc = defaultLoadConfig
	SaveConfig        SaveConfigFunc = defaultSaveConfig
	SaveOnClose                      = false

	TagName       = "bind"
	TagMapDefault = ""
)

var (
	ErrHandlerNotSet = errors.New("handler not set")
)

type MappedConfiguration map[string]interface{}

// Passed *MappedConfiguration
type LoadConfigFunc func(mc *MappedConfiguration) error
type SaveConfigFunc func(mc *MappedConfiguration) error

type RegisterFunc func(parent string, fieldType reflect.StructField, fieldValue reflect.Value)

type funcUpdater struct {
	In  func()
	Out func()
}

type item struct {
	bindEnvArgs bool
	bindConf    bool
	val         interface{}
}

func BindArgs(st interface{}, key string) (err error) {
	mid, in, out := Link(st)
	mappedConfUpdater[key] = &funcUpdater{
		In: in, Out: out,
	}
	registered[key] = &item{
		bindEnvArgs: true,
		val:         mid,
	}
	return
}

func BindConf(st interface{}, key string) (err error) {
	mid, in, out := Link(st)
	mappedConfUpdater[key] = &funcUpdater{
		In: in, Out: out,
	}
	registered[key] = &item{
		bindConf: true,
		val:      mid,
	}
	return
}

func BindArgsConf(st interface{}, key string) (err error) {
	mid, in, out := Link(st)
	mappedConfUpdater[key] = &funcUpdater{
		In: in, Out: out,
	}
	registered[key] = &item{
		bindEnvArgs: true,
		bindConf:    true,
		val:         mid,
	}
	return
}

func Init() (err error) {
	// order priority:
	// 1. defaulf value         - runtime
	// 2. args value            - tag "args"
	// 3. env value             - tag "env"
	// 4. configuration value   - from conf file

	for k, s := range registered {
		if s.bindEnvArgs {
			defer addBindArgs(k, s)
		}
		if s.bindConf {
			mappedConf[k] = s.val
		}
	}
	var t MappedConfiguration
	defer func() {
		setBackMap(&mappedConf, t)
		loadReupdate()
	}()
	if err = LoadConfig(&t); err != nil {
		return
	}
	return
}

func Save() (err error) {
	if SaveConfig == nil {
		return ErrHandlerNotSet
	}
	saveReupdate()
	if err = SaveConfig(&mappedConf); err != nil {
		err = errors.Wrap(err, "binder.Save")
		return
	}
	return
}

func In() {
	loadReupdate()
}

func Out() {
	saveReupdate()
}

func Close() (err error) {
	if SaveOnClose {
		if err = Save(); err != nil {
			panic(err)
		}
	}
	return
}

func setBackMap(dst *MappedConfiguration, val MappedConfiguration) {
	ds := reflect.ValueOf(dst)
	if ds.Kind() == reflect.Ptr {
		ds = ds.Elem()
	}
	iface := ds.Interface().(MappedConfiguration)
	for k, orig := range iface {
		src := val[k]
		// spew.Dump(orig)

		if src == nil {
			// set back to component ?
			continue
		}

		iorig := reflect.ValueOf(orig)
		// if iorig.Kind() == reflect.Ptr {
		// 	iorig = iorig.Elem()
		// }
		ival := reflect.ValueOf(src)
		if ival.Kind() == reflect.Ptr {
			ival = ival.Elem()
		}
		// fmt.Printf("IORIG ")
		// spew.Dump(iorig.Interface())
		ifaceToStruct(ival, iorig)
		// spew.Dump(iorig)

		// fmt.Printf(">> SET BACK ival %+#v\n", ival)
		// fmt.Printf(">> SET BACK iorg %+#v\n", iorig)
	}
}

func ifaceToStruct(ival reflect.Value, iorig reflect.Value) {
	if iorig.Kind() != reflect.Struct && (iorig.Kind() != reflect.Ptr && !iorig.IsNil() && iorig.Elem().Kind() == reflect.Struct) {
		conv := ival.Convert(iorig.Type())
		iorig.Set(conv)
		return
	}

	// check if ival is map[string]interface{}
	vk := ival.Type().Key().Kind()
	vv := ival.Type().Elem().Kind()
	if vk != reflect.String && vv != reflect.Interface {
		panic(fmt.Sprintf("convert is not supported : has key %s and val %s", vk, vv))
	}

	// with the help of mapstructure! :D
	dec, err := mapstructure.NewDecoder(&mapstructure.DecoderConfig{
		// result
		Result: iorig.Interface(),
		// use bind:"" instead of mapstructure:""
		TagName: TagName,
	})
	if err != nil {
		panic(errors.Wrap(err, "mapstructure init error"))
	}
	if err = dec.Decode(ival.Interface()); err != nil {
		panic(errors.Wrap(err, "mapstructure error"))
	}
	// spew.Dump(iorig.Interface())
}

func addBindArgs(key string, s *item) {
	v := reflect.ValueOf(s.val)
	_ = v
	instFields(key, v, wrapperUnwind(wrapperOSEnv(RegisterCmdArgs)))
}

func instFields(parent string, v reflect.Value, fc RegisterFunc) {
	t := v.Type()
	if t.Kind() == reflect.Ptr {
		if v.IsNil() {
			FillValue(v)
		}
		instFields(parent, v.Elem(), fc)
		return
	}
	if t.Kind() == reflect.Struct {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			t := f.Type
			fd := v.Field(i)

			fd, _ = UnwindValue(fd, true, true)
			t, _ = UnwindType(t, false)

			bindTagVal := f.Tag.Get(TagName)
			if bindTagVal != "" {
				bindTagValParsed := SplitTagValue(bindTagVal)
				if len(bindTagValParsed) > 0 {
					bindTagVal = bindTagValParsed[0]
				}
			}
			bindName := f.Name
			if bindTagVal != "" {
				bindName = bindTagVal
			}

			if bindName == "-" {
				// skip field if bind:"-"
				continue
			}

			sub := parent
			if sub == "" {
				sub = bindName
			} else {
				sub = sub + "." + bindName
			}
			if t.Kind() == reflect.Struct {
				instFields(sub, fd, fc)
				continue
			}
			//
			fc(parent, f, fd)
		}
	}
}

func loadReupdate() {
	for _, v := range mappedConfUpdater {
		v.In()
	}
}

func saveReupdate() {
	for _, v := range mappedConfUpdater {
		v.Out()
	}
}

func wrapperUnwind(f RegisterFunc) RegisterFunc {
	return func(parent string, fieldType reflect.StructField, fieldValue reflect.Value) {
		defer f(parent, fieldType, fieldValue)

		for fieldValue.Kind() == reflect.Ptr {
			if t := fieldValue.Elem(); t.Kind() != reflect.Ptr {
				break
			} else {
				fieldValue = t
			}
		}

	}
}

func wrapperOSEnv(f RegisterFunc) RegisterFunc {
	return func(parent string, fieldType reflect.StructField, fieldValue reflect.Value) {
		defer f(parent, fieldType, fieldValue)

		environName := fieldType.Tag.Get("environ")
		envName := fieldType.Tag.Get("env")

		environNameParsed := SplitTagValue(environName)
		envNameParsed := SplitTagValue(envName)

		if len(envNameParsed) > 0 {
			envName = envNameParsed[0]
		}

		if len(environNameParsed) > 0 && environNameParsed[0] != "" { // dedicated name
			environName = environNameParsed[0]
			envName = environName
		} else if envName == "" {
			// don't lookup any
			return
		} else {
			if parent != "" {
				envName = parent + "." + envName
			}
		}

		val, ok := os.LookupEnv(envName)
		if ok {
			v := convertStringToType(val, fieldValue.Type())
			fieldValue.Elem().Set(v)
		}
	}
}

func convertStringToType(s string, t reflect.Type) reflect.Value {
	t, _ = UnwindType(t, false)
	var ret interface{} = s
	switch t.Kind() {
	case reflect.Bool:
		ret, _ = strconv.ParseBool(s)
	case reflect.Int:
		ret, _ = strconv.Atoi(s)
	case reflect.Int16:
		tmp, _ := strconv.ParseInt(s, 10, 16)
		ret = tmp
	case reflect.Int32:
		tmp, _ := strconv.ParseInt(s, 10, 32)
		ret = tmp
	case reflect.Int64:
		ret, _ = strconv.ParseInt(s, 10, 64)
	case reflect.Float32:
		tmp, _ := strconv.ParseFloat(s, 32)
		ret = tmp
	case reflect.Float64:
		ret, _ = strconv.ParseFloat(s, 64)
	case reflect.Uint:
	case reflect.Uint32:
		tmp, _ := strconv.ParseUint(s, 10, 32)
		ret = tmp
	case reflect.Uint64:
		tmp, _ := strconv.ParseUint(s, 10, 64)
		ret = tmp
	}
	return reflect.ValueOf(ret)
}
