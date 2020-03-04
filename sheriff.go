package sheriff

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	version "github.com/hashicorp/go-version"
)

// Options determine which struct fields are being added to the output map.
type Options struct {
	// Groups determine which fields are getting marshalled based on the groups tag.
	// A field with multiple groups (comma-separated) will result in marshalling of that
	// field if one of their groups is specified.
	Groups []string
	// ApiVersion sets the API version to use when marshalling.
	// The tags `since` and `until` use the API version setting.
	// Specifying the API version as "1.0.0" and having an until setting of "2"
	// will result in the field being marshalled.
	// Specifying a since setting of "2" with the same API version specified,
	// will not marshal the field.
	ApiVersion *version.Version

	// OutputFieldWithNoGroup causes fields with no group tag to be included in
	// the output. Default behavior is to skip fields without a group tag.
	// Fields with group tags that do not match any of the names in Groups will
	// still be ignored. This is useful if an object has a large number of
	// fields, and only a small number are tagged as optional additional output.
	OutputFieldsWithNoGroup bool

	// InheritGroups causes any group applied to a struct-type field to
	// propagate to all fields of that struct.
	InheritGroups bool
}

// MarshalInvalidTypeError is an error returned to indicate the wrong type has been
// passed to Marshal.
type MarshalInvalidTypeError struct {
	// t reflects the type of the data
	t reflect.Kind
	// data contains the passed data itself
	data interface{}
}

func (e MarshalInvalidTypeError) Error() string {
	return fmt.Sprintf("marshaller: Unable to marshal type %s. Struct required.", e.t)
}

// Marshaller is the interface models have to implement in order to conform to marshalling.
type Marshaller interface {
	Marshal(options *Options) (interface{}, error)
}

// Marshal encodes the passed data into a map which can be used to pass to json.Marshal().
//
// If the passed argument `data` is a struct, the return value will be of type `map[string]interface{}`.
// In all other cases we can't derive the type in a meaningful way and is therefore an `interface{}`.
func Marshal(options *Options, data interface{}) (interface{}, error) {
	groups := make(groupSet)
	groups.incrementGroups(options.Groups)
	parents := make(groupSet)
	return marshalObject(options, data, groups, parents, false)
}

func marshalObject(options *Options, data interface{}, groups, parents groupSet, embeddedParents bool) (interface{}, error) {
	v := reflect.ValueOf(data)
	t := v.Type()

	if t.Kind() == reflect.Ptr {
		// follow pointer
		t = t.Elem()
	}
	if v.Kind() == reflect.Ptr {
		// follow pointer
		v = v.Elem()
	}

	if t.Kind() != reflect.Struct {
		return marshalValue(options, v, groups, parents, false)
	}

	dest := make(map[string]interface{})

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		val := v.Field(i)

		jsonTag, jsonOpts := parseTag(field.Tag.Get("json"))

		// If no json tag is provided, use the field Name
		if jsonTag == "" {
			jsonTag = field.Name
		}

		if jsonTag == "-" {
			continue
		}
		if jsonOpts.Contains("omitempty") && isEmptyValue(val) {
			continue
		}
		// skip unexported fields
		if !val.IsValid() || !val.CanInterface() {
			continue
		}

		// if there is an anonymous field which is a struct
		// we want the childs exposed at the toplevel to be
		// consistent with the embedded json marshaller
		if val.Kind() == reflect.Ptr {
			val = val.Elem()
		}

		// we can skip the group checkif if the field is a composition field
		isEmbeddedField := field.Anonymous && val.Kind() == reflect.Struct
		var groupNames []string
		checkGroups := len(options.Groups) > 0 || (options.InheritGroups && len(parents) > 0) || options.OutputFieldsWithNoGroup
		shouldShowFromGroup := true
		if checkGroups {
			if field.Tag.Get("groups") != "" {
				groupNames = strings.Split(field.Tag.Get("groups"), ",")
			}
			hasExactMatch := groups.containsAny(groupNames)
			hasParentMatch := false
			if options.InheritGroups {
				hasParentMatch = parents.containsAny(options.Groups)
			} else if embeddedParents && len(groupNames) == 0 {
				hasParentMatch = parents.containsAny(options.Groups)
			}
			hasNoGroup := (len(groupNames) == 0)
			shouldShowFromGroup = hasExactMatch || hasParentMatch || (hasNoGroup && options.OutputFieldsWithNoGroup) || isEmbeddedField
		}

		shouldShowFromSince := true
		if since := field.Tag.Get("since"); since != "" {
			sinceVersion, err := version.NewVersion(since)
			if err != nil {
				return nil, err
			}
			if options.ApiVersion.LessThan(sinceVersion) {
				shouldShowFromSince = false
			}
		}

		shouldShowFromUntil := true
		if until := field.Tag.Get("until"); until != "" {
			untilVersion, err := version.NewVersion(until)
			if err != nil {
				return nil, err
			}
			if options.ApiVersion.GreaterThan(untilVersion) {
				shouldShowFromUntil = false
			}
		}

		if options.InheritGroups || isEmbeddedField {
			parents.incrementGroups(groupNames)
		}
		v, err := marshalValue(options, val, groups, parents, isEmbeddedField)
		if options.InheritGroups || isEmbeddedField {
			parents.decrementGroups(groupNames)
		}
		if err != nil {
			return nil, err
		}
		if shouldShowFromGroup && shouldShowFromSince && shouldShowFromUntil {
			nestedVal, ok := v.(map[string]interface{})
			if isEmbeddedField && ok {
				for k, v := range nestedVal {
					dest[k] = v
				}
			} else {
				dest[jsonTag] = v
			}
		}
	}

	return dest, nil
}

// marshalValue is being used for getting the actual value of a field.
//
// There is support for types implementing the Marshaller interface, arbitrary structs, slices, maps and base types.
func marshalValue(options *Options, v reflect.Value, groups, parents groupSet, embeddedParents bool) (interface{}, error) {
	// return nil on nil pointer struct fields
	if !v.IsValid() || !v.CanInterface() {
		return nil, nil
	}
	val := v.Interface()

	if marshaller, ok := val.(Marshaller); ok {
		return marshaller.Marshal(options)
	}
	// types which are e.g. structs, slices or maps and implement one of the following interfaces should not be
	// marshalled by sheriff because they'll be correctly marshalled by json.Marshal instead.
	// Otherwise (e.g. net.IP) a byte slice may be output as a list of uints instead of as an IP string.
	switch val.(type) {
	case json.Marshaler, encoding.TextMarshaler, fmt.Stringer:
		return val, nil
	}
	k := v.Kind()

	if k == reflect.Ptr {
		v = v.Elem()
		val = v.Interface()
		k = v.Kind()
	}

	if k == reflect.Interface || k == reflect.Struct {
		return marshalObject(options, val, groups, parents, embeddedParents)
	}
	if k == reflect.Slice {
		l := v.Len()
		dest := make([]interface{}, l)
		for i := 0; i < l; i++ {
			d, err := marshalValue(options, v.Index(i), groups, parents, embeddedParents)
			if err != nil {
				return nil, err
			}
			dest[i] = d
		}
		return dest, nil
	}
	if k == reflect.Map {
		mapKeys := v.MapKeys()
		if len(mapKeys) == 0 {
			return nil, nil
		}
		if mapKeys[0].Kind() != reflect.String {
			return nil, MarshalInvalidTypeError{t: mapKeys[0].Kind(), data: val}
		}
		dest := make(map[string]interface{})
		for _, key := range mapKeys {
			d, err := marshalValue(options, v.MapIndex(key), groups, parents, embeddedParents)
			if err != nil {
				return nil, err
			}
			dest[key.Interface().(string)] = d
		}
		return dest, nil
	}
	return val, nil
}

// contains check if a given key is contained in a slice of strings.
func contains(key string, list []string) bool {
	for _, innerKey := range list {
		if key == innerKey {
			return true
		}
	}
	return false
}

// listContains operates on two string slices and checks if one of the strings in `a`
// is contained in `b`.
func listContains(a []string, b []string) bool {
	for _, key := range a {
		if contains(key, b) {
			return true
		}
	}
	return false
}
