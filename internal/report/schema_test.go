// Test data: No data at all. This walks the document type and checks the rules its shape has
// to obey.

package report

import (
	"reflect"
	"strings"
	"testing"
)

// walkSchema visits every struct field reachable from the document type,
// passing a dotted path and the field, and skipping types that opt out of
// reflection by handling their own JSON.
func walkSchema(t *testing.T, typ reflect.Type, path string, visit func(path string, field reflect.StructField)) {
	t.Helper()

	switch typ.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		walkSchema(t, typ.Elem(), path, visit)
		return

	case reflect.Map:
		walkSchema(t, typ.Elem(), path, visit)
		return

	case reflect.Struct:
		// A type with its own marshaller is a leaf: its internals never appear
		// in the document.
		if implementsJSONMarshaler(typ) {
			return
		}

		for i := range typ.NumField() {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}

			name := jsonName(field)
			child := path + "." + name
			visit(child, field)
			walkSchema(t, field.Type, child, visit)
		}
		return

	default:
		return
	}
}

func implementsJSONMarshaler(typ reflect.Type) bool {
	marshaler := reflect.TypeOf((*interface{ MarshalJSON() ([]byte, error) })(nil)).Elem()

	return typ.Implements(marshaler) || reflect.PointerTo(typ).Implements(marshaler)
}

// jsonName returns the field's name as it appears in the document.
func jsonName(field reflect.StructField) string {
	tag := field.Tag.Get("json")
	if tag == "" {
		return field.Name
	}

	return strings.Split(tag, ",")[0]
}

// A float64 in the document would mean a number that had been through binary
// floating point, and therefore a number that might have been rounded. Prices
// carry the API's own text and the statistics are built by integer arithmetic,
// so there is no legitimate reason for one to appear.
func TestNoFloatReachesTheDocument(t *testing.T) {
	walkSchema(t, reflect.TypeOf(Document{}), "document", func(path string, field reflect.StructField) {
		typ := field.Type
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}

		if typ.Kind() == reflect.Float32 || typ.Kind() == reflect.Float64 {
			t.Errorf("%s is a %s: no number in the document may be a float", path, typ.Kind())
		}
	})
}

// A consumer has to be able to tell "no liquidity" from "we did not find out".
// omitempty destroys that distinction, and it does so silently, on whichever
// field happens to be empty in a given run.
func TestNoFieldIsEverOmitted(t *testing.T) {
	walkSchema(t, reflect.TypeOf(Document{}), "document", func(path string, field reflect.StructField) {
		if strings.Contains(field.Tag.Get("json"), "omitempty") {
			t.Errorf("%s is tagged omitempty: unknown values must be null, not absent", path)
		}
	})
}

// Every field needs a name chosen for the document rather than inherited from
// Go, or the contract changes whenever a field is renamed in the code.
func TestEveryFieldHasAnExplicitName(t *testing.T) {
	walkSchema(t, reflect.TypeOf(Document{}), "document", func(path string, field reflect.StructField) {
		if field.Tag.Get("json") == "" {
			t.Errorf("%s has no json tag", path)
		}
	})
}

// The names are snake_case throughout. This is cosmetic, but a document with
// two conventions in it is a document someone will eventually mis-parse.
func TestFieldNamesAreSnakeCase(t *testing.T) {
	walkSchema(t, reflect.TypeOf(Document{}), "document", func(path string, field reflect.StructField) {
		name := jsonName(field)
		if name != strings.ToLower(name) {
			t.Errorf("%s is not lower case", path)
		}
		if strings.Contains(name, "-") {
			t.Errorf("%s uses a hyphen, not an underscore", path)
		}
	})
}

// schemaFieldNames lists every field name in the document, for the
// documentation cross-check.
func schemaFieldNames(t *testing.T) map[string]bool {
	t.Helper()

	names := make(map[string]bool)
	walkSchema(t, reflect.TypeOf(Document{}), "document", func(_ string, field reflect.StructField) {
		names[jsonName(field)] = true
	})

	return names
}
