package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// Invoke calls one of the functions returned by Methods with JSON encoded
// arguments and returns its JSON encoded result.
func Invoke(fn interface{}, args []json.RawMessage) (json.RawMessage, error) {
	v := reflect.ValueOf(fn)
	if v.Kind() != reflect.Func {
		return nil, errors.New("unknown method")
	}
	t := v.Type()
	// Missing trailing arguments get their zero value.
	if len(args) > t.NumIn() {
		return nil, fmt.Errorf("expected %d arguments, got %d", t.NumIn(), len(args))
	}
	in := make([]reflect.Value, t.NumIn())
	for i := range in {
		p := reflect.New(t.In(i))
		if i < len(args) && len(args[i]) > 0 && string(args[i]) != "null" {
			if err := json.Unmarshal(args[i], p.Interface()); err != nil {
				return nil, fmt.Errorf("argument %d: %v", i+1, err)
			}
		}
		in[i] = p.Elem()
	}
	out := v.Call(in)
	errType := reflect.TypeOf((*error)(nil)).Elem()
	var result interface{}
	for _, o := range out {
		if o.Type().Implements(errType) {
			if !o.IsNil() {
				return nil, o.Interface().(error)
			}
			continue
		}
		result = o.Interface()
	}
	return json.Marshal(result)
}
