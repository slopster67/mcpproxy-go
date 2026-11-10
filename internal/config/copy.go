package config

import "reflect"

// CopyConfig makes an independent runtime snapshot, including database entities.
// It never serializes them into the deployment settings document.
func CopyConfig(cfg *Config) *Config {
	if cfg == nil {
		return nil
	}
	result := deepCopyExported(reflect.ValueOf(cfg).Elem()).Interface().(Config)
	return &result
}

func deepCopyExported(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(deepCopyExported(v.Elem()))
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(deepCopyExported(v.Index(i)))
		}
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), deepCopyExported(iter.Value()))
		}
		return out
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(deepCopyExported(v.Elem()))
		return out
	case reflect.Struct:
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			if t.Field(i).IsExported() {
				out.Field(i).Set(deepCopyExported(v.Field(i)))
			}
		}
		return out
	default:
		return v
	}
}

// CopyServerConfig uses the same deep-copy implementation as runtime snapshots.
func CopyServerConfig(cfg *ServerConfig) *ServerConfig {
	if cfg == nil {
		return nil
	}
	result := deepCopyExported(reflect.ValueOf(cfg).Elem()).Interface().(ServerConfig)
	return &result
}
