// Copyright (c) 2013 Kelsey Hightower. All rights reserved.
// Use of this source code is governed by the MIT License that can be found in
// the LICENSE file.

package kkonfig

import (
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidSpecification indicates that a specification is of the wrong type.
var ErrInvalidSpecification = errors.New("specification must be a struct pointer")

// A ParseError occurs when an environment variable cannot be converted to
// the type required by a struct field during assignment.
type ParseError struct {
	KeyName   string
	FieldName string
	TypeName  string
	Value     string
	Err       error
}

// Decoder has the same semantics as Setter, but takes higher precedence.
// It is provided for historical compatibility.
type Decoder interface {
	Decode(value string) error
}

// Setter is implemented by types can self-deserialize values.
// Any type that implements flag.Value also implements Setter.
type Setter interface {
	Set(value string) error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("envconfig.Process: assigning %[1]s to %[2]s: converting '%[3]s' to type %[4]s. details: %[5]s", e.KeyName, e.FieldName, e.Value, e.TypeName, e.Err)
}

func processDefaultValues(spec interface{}) error {
	s := reflect.ValueOf(spec).Elem()
	typeOfSpec := s.Type()
	for i := 0; i < s.NumField(); i++ {
		f := s.Field(i)
		ftype := typeOfSpec.Field(i)
		if !f.CanSet() || ftype.Tag.Get("ignored") == "true" {
			continue
		}

		for f.Kind() == reflect.Ptr {
			if f.IsNil() {
				if f.Type().Elem().Kind() != reflect.Struct {
					// nil pointer to a non-struct: leave it alone
					break
				}
				// nil pointer to struct: create a zero instance
				f.Set(reflect.New(f.Type().Elem()))
			}
			f = f.Elem()
		}

		if f.Kind() == reflect.Struct {
			embeddedPtr := f.Addr().Interface()
			if err := processDefaultValues(embeddedPtr); err != nil {
				return err
			}
			f.Set(reflect.ValueOf(embeddedPtr).Elem())
			continue
		}

		if value, ok := ftype.Tag.Lookup("default"); ok {
			if err := processField(value, f); err != nil {
				return &ParseError{
					FieldName: ftype.Name,
					TypeName:  f.Type().String(),
					Value:     value,
					Err:       err,
				}
			}
		}

	}
	return nil
}

func processJson(configPaths []string, spec interface{}) error {
	// Parse potential json files into the specification
	if configPaths != nil {
		for _, path := range configPaths {
			if jsonBytes, err := ioutil.ReadFile(path); err == nil {
				if json.Unmarshal(jsonBytes, spec) != nil {
					continue
				}
			}
		}
	}
	return nil
}

func processEnvironmentValues(prefix string, spec interface{}) error {
	s := reflect.ValueOf(spec).Elem()
	typeOfSpec := s.Type()
	for i := 0; i < s.NumField(); i++ {
		f := s.Field(i)
		ftype := typeOfSpec.Field(i)
		if !f.CanSet() || ftype.Tag.Get("ignored") == "true" {
			continue
		}

		for f.Kind() == reflect.Ptr {
			if f.IsNil() {
				if f.Type().Elem().Kind() != reflect.Struct {
					// nil pointer to a non-struct: leave it alone
					break
				}
				// nil pointer to struct: create a zero instance
				f.Set(reflect.New(f.Type().Elem()))
			}
			f = f.Elem()
		}

		fieldName := ftype.Name
		if alt := ftype.Tag.Get("envconfig"); alt != "" {
			fieldName = alt
		}

		key := fieldName
		// If a prefix has been specified, modify the key from "key" to "prefix_key"
		if prefix != "" {
			key = fmt.Sprintf("%s_%s", prefix, key)
		}

		// Environment variables should be uppercase, modify from "prefix_key" to "PREFIX_KEY"
		key = strings.ToUpper(key)

		// The current field is a struct, continue going through that struct but with a new prefix
		if f.Kind() == reflect.Struct {
			// honor Decode if present
			if decoderFrom(f) == nil && setterFrom(f) == nil && textUnmarshaler(f) == nil {
				innerPrefix := prefix
				if !ftype.Anonymous {
					innerPrefix = key
				}

				embeddedPtr := f.Addr().Interface()
				if err := processEnvironmentValues(innerPrefix, embeddedPtr); err != nil {
					return err
				}
				f.Set(reflect.ValueOf(embeddedPtr).Elem())

				continue
			}
		}

		// `os.Getenv` cannot differentiate between an explicitly set empty value
		// and an unset value. `os.LookupEnv` is preferred to `syscall.Getenv`,
		// but it is only available in go1.5 or newer. We're using Go build tags
		// here to use os.LookupEnv for >=go1.5
		if value, ok := lookupEnv(key); ok {
			if err := processField(value, f); err != nil {
				return &ParseError{
					KeyName:   key,
					FieldName: fieldName,
					TypeName:  f.Type().String(),
					Value:     value,
					Err:       err,
				}
			}
		}

		// fmt.Printf("Env value: %s: %#v\n", fieldName, value)

		/*
			req := ftype.Tag.Get("required")
			if !ok && def == "" && !set {
				if req == "true" {
					return fmt.Errorf("required key %s missing value", key)
				}
				continue
			}
		*/

	}
	return nil
}

// Process populates the specified struct in the following steps:
// 1. Fill in with default values
// 2. Read from given config files
// 3. Read from environment variables
// TODO: Parse values in three steps instead of just 1. Less performant but more unsure
func Process(prefix string, configPaths []string, spec interface{}) error {
	// Sanity check on struct to make sure it's a pointer to a struct
	s := reflect.ValueOf(spec)

	if s.Kind() != reflect.Ptr {
		return ErrInvalidSpecification
	}
	s = s.Elem()
	if s.Kind() != reflect.Struct {
		return ErrInvalidSpecification
	}

	err := processDefaultValues(spec)
	if err != nil {
		return err
	}
	err = processJson(configPaths, spec)
	if err != nil {
		return err
	}
	err = processEnvironmentValues(prefix, spec)
	if err != nil {
		return err
	}

	return nil
}

// MustProcess is the same as Process but panics if an error occurs
func MustProcess(prefix string, configPaths []string, spec interface{}) {
	if err := Process(prefix, configPaths, spec); err != nil {
		panic(err)
	}
}

func processField(value string, field reflect.Value) error {
	typ := field.Type()

	decoder := decoderFrom(field)
	if decoder != nil {
		return decoder.Decode(value)
	}
	// look for Set method if Decode not defined
	setter := setterFrom(field)
	if setter != nil {
		return setter.Set(value)
	}

	if t := textUnmarshaler(field); t != nil {
		return t.UnmarshalText([]byte(value))
	}

	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
		if field.IsNil() {
			field.Set(reflect.New(typ))
		}
		field = field.Elem()
	}

	switch typ.Kind() {
	case reflect.String:
		field.SetString(value)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var (
			val int64
			err error
		)
		if field.Kind() == reflect.Int64 && typ.PkgPath() == "time" && typ.Name() == "Duration" {
			var d time.Duration
			d, err = time.ParseDuration(value)
			val = int64(d)
		} else {
			val, err = strconv.ParseInt(value, 0, typ.Bits())
		}
		if err != nil {
			return err
		}

		field.SetInt(val)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		val, err := strconv.ParseUint(value, 0, typ.Bits())
		if err != nil {
			return err
		}
		field.SetUint(val)
	case reflect.Bool:
		val, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		field.SetBool(val)
	case reflect.Float32, reflect.Float64:
		val, err := strconv.ParseFloat(value, typ.Bits())
		if err != nil {
			return err
		}
		field.SetFloat(val)
	case reflect.Slice:
		vals := strings.Split(value, ",")
		sl := reflect.MakeSlice(typ, len(vals), len(vals))
		for i, val := range vals {
			err := processField(val, sl.Index(i))
			if err != nil {
				return err
			}
		}
		field.Set(sl)
	}

	return nil
}

func interfaceFrom(field reflect.Value, fn func(interface{}, *bool)) {
	// it may be impossible for a struct field to fail this check
	if !field.CanInterface() {
		return
	}
	var ok bool
	fn(field.Interface(), &ok)
	if !ok && field.CanAddr() {
		fn(field.Addr().Interface(), &ok)
	}
}

func decoderFrom(field reflect.Value) (d Decoder) {
	interfaceFrom(field, func(v interface{}, ok *bool) { d, *ok = v.(Decoder) })
	return d
}

func setterFrom(field reflect.Value) (s Setter) {
	interfaceFrom(field, func(v interface{}, ok *bool) { s, *ok = v.(Setter) })
	return s
}

func textUnmarshaler(field reflect.Value) (t encoding.TextUnmarshaler) {
	interfaceFrom(field, func(v interface{}, ok *bool) { t, *ok = v.(encoding.TextUnmarshaler) })
	return t
}
