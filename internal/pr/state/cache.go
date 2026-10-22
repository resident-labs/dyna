package state

import (
	"reflect"
	"sync"
)

// cloneState copies the JSON schema's mutable maps, slices and pointers. Scalar
// values, including strings and time.Time's private immutable data, can be shared.
func cloneState(st State) State {
	return cloneStateValue(reflect.ValueOf(st)).Interface().(State)
}

// deepFields caches, per type, the exported struct fields that hold mutable
// memory. A type without an entry in its list is copied in one assignment.
var deepFields sync.Map // reflect.Type -> []int

// mutable reports whether values of t hold maps, slices or pointers that a
// clone must copy. Unexported fields are shared, as cloneStateValue shares them.
func mutable(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
		return true
	case reflect.Struct:
		return len(mutableFields(t)) > 0
	}
	return false
}

func mutableFields(t reflect.Type) []int {
	if fields, ok := deepFields.Load(t); ok {
		return fields.([]int)
	}
	fields := []int{}
	for i := 0; i < t.NumField(); i++ {
		if f := t.Field(i); f.IsExported() && mutable(f.Type) {
			fields = append(fields, i)
		}
	}
	deepFields.Store(t, fields)
	return fields
}

func cloneStateValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type().Elem())
		out.Elem().Set(cloneStateValue(v.Elem()))
		return out
	case reflect.Interface:
		if v.IsNil() {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(cloneStateValue(v.Elem()))
		return out
	case reflect.Map:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		deep := mutable(v.Type().Elem())
		iter := v.MapRange()
		for iter.Next() {
			if deep {
				out.SetMapIndex(iter.Key(), cloneStateValue(iter.Value()))
			} else {
				out.SetMapIndex(iter.Key(), iter.Value())
			}
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return v
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		if !mutable(v.Type().Elem()) {
			reflect.Copy(out, v)
			return out
		}
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneStateValue(v.Index(i)))
		}
		return out
	case reflect.Struct:
		fields := mutableFields(v.Type())
		if len(fields) == 0 {
			return v
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(v)
		for _, i := range fields {
			out.Field(i).Set(cloneStateValue(v.Field(i)))
		}
		return out
	default:
		return v
	}
}
