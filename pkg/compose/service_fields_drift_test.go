package compose

import (
	"reflect"
	"testing"
)

// A Compose service key has to be spelled out in SIX places: the ServiceDocument
// struct (the file's shape), the Service struct (the internal shape),
// supportedServiceFields (the warn-don't-drop allow-list), serviceFromDocument
// and Service.toDocument (the two directions of the copy), and mergeService (the
// across-`-f`-files merge). Nothing in the compiler notices when one is missed,
// and the failure modes are silent: miss serviceFromDocument and the key is
// dropped outright, miss mergeService and it silently stops merging (mergeService
// opens with `out := base`, so an unhandled key keeps base's value and the
// override's is discarded), miss supportedServiceFields and a supported key gets
// warned about as unsupported.
//
// The tests below close that whole class by reflection, so a new key is caught
// at `go test` time rather than by a user. They assert structure only — no
// behaviour — so they cost nothing to keep and should never need editing when a
// key is added correctly.

// maxFillDepth bounds nonZeroValue's recursion. Nothing in the service types
// nests this deeply; the bound exists so a self-referential type could not
// recurse forever.
const maxFillDepth = 6

// nonZeroValue returns a value of type t with every settable field, element, and
// entry populated with a distinguishable non-zero value. It is deliberately
// value-agnostic: these tests care whether a field was CARRIED, not what it
// holds, so any non-zero value will do.
func nonZeroValue(t reflect.Type, depth int) reflect.Value {
	v := reflect.New(t).Elem()
	if depth > maxFillDepth {
		return v
	}
	switch t.Kind() {
	case reflect.Bool:
		v.SetBool(true)
	case reflect.String:
		v.SetString("x")
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	case reflect.Pointer:
		p := reflect.New(t.Elem())
		p.Elem().Set(nonZeroValue(t.Elem(), depth+1))
		v.Set(p)
	case reflect.Slice:
		s := reflect.MakeSlice(t, 1, 1)
		s.Index(0).Set(nonZeroValue(t.Elem(), depth+1))
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(t)
		m.SetMapIndex(nonZeroValue(t.Key(), depth+1), nonZeroValue(t.Elem(), depth+1))
		v.Set(m)
	case reflect.Struct:
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" { // unexported: not settable
				continue
			}
			v.Field(i).Set(nonZeroValue(f.Type, depth+1))
		}
	}
	return v
}

// fieldNames returns the exported field names of a struct type.
func fieldNames(t reflect.Type) map[string]bool {
	out := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		if f := t.Field(i); f.PkgPath == "" {
			out[f.Name] = true
		}
	}
	return out
}

// zeroFields returns the names of v's exported fields that are still zero,
// skipping any name in exempt.
func zeroFields(v reflect.Value, exempt map[string]bool) []string {
	var out []string
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" || exempt[f.Name] {
			continue
		}
		if v.Field(i).IsZero() {
			out = append(out, f.Name)
		}
	}
	return out
}

// TestServiceAndDocumentFieldsMatch asserts Service and ServiceDocument carry
// the same field lineup, which their own doc comments promise ("Its field lineup
// mirrors ServiceDocument").
func TestServiceAndDocumentFieldsMatch(t *testing.T) {
	doc := fieldNames(reflect.TypeOf(ServiceDocument{}))
	svc := fieldNames(reflect.TypeOf(Service{}))
	for name := range doc {
		if !svc[name] {
			t.Errorf("ServiceDocument.%s has no counterpart on Service", name)
		}
	}
	for name := range svc {
		if !doc[name] {
			t.Errorf("Service.%s has no counterpart on ServiceDocument", name)
		}
	}
}

// TestSupportedServiceFieldsMatchesDocument asserts the warn-don't-drop
// allow-list and the ServiceDocument json tags are the same set. A tag missing
// from the map makes a supported key warn as unsupported; a map key with no tag
// is a stale entry that silences a warning for a key nothing reads.
func TestSupportedServiceFieldsMatchesDocument(t *testing.T) {
	dt := reflect.TypeOf(ServiceDocument{})
	tags := make(map[string]bool, dt.NumField())
	for i := 0; i < dt.NumField(); i++ {
		f := dt.Field(i)
		if f.PkgPath != "" {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "" || tag == "-" {
			t.Errorf("ServiceDocument.%s has no json tag, so no Compose key maps onto it", f.Name)
			continue
		}
		tags[tag] = true
		if _, ok := supportedServiceFields[tag]; !ok {
			t.Errorf("%q (ServiceDocument.%s) missing from supportedServiceFields: the key would warn as unsupported", tag, f.Name)
		}
	}
	for key := range supportedServiceFields {
		if !tags[key] {
			t.Errorf("supportedServiceFields has %q, which no ServiceDocument field declares", key)
		}
	}
}

// TestServiceFromDocumentCopiesEveryField asserts serviceFromDocument carries
// every field across. A field it forgets is dropped entirely: the Compose file
// says it, nothing downstream sees it.
func TestServiceFromDocumentCopiesEveryField(t *testing.T) {
	doc := nonZeroValue(reflect.TypeOf(ServiceDocument{}), 0).Interface().(ServiceDocument)
	got := reflect.ValueOf(serviceFromDocument(doc))
	for _, name := range zeroFields(got, nil) {
		t.Errorf("serviceFromDocument drops %s: set on the document, zero on the Service", name)
	}
}

// TestToDocumentCopiesEveryField asserts the reverse direction carries every
// field. toDocument feeds the merge path (LoadDocumentWithOptions, extends,
// include), so a field it forgets is lost whenever a service is re-serialised.
func TestToDocumentCopiesEveryField(t *testing.T) {
	svc := nonZeroValue(reflect.TypeOf(Service{}), 0).Interface().(Service)
	got := reflect.ValueOf(svc.toDocument())
	for _, name := range zeroFields(got, nil) {
		t.Errorf("Service.toDocument drops %s: set on the Service, zero on the document", name)
	}
}

// TestMergeServiceCarriesEveryOverrideField asserts mergeService applies every
// field of the override. Merging a fully-populated override onto an EMPTY base
// makes an unhandled key visible: mergeService opens with `out := base`, so any
// field it never touches comes back zero.
func TestMergeServiceCarriesEveryOverrideField(t *testing.T) {
	override := nonZeroValue(reflect.TypeOf(ServiceDocument{}), 0).Interface().(ServiceDocument)
	// Extends is deliberately not merged: it is a load-time directive that
	// resolveExtends expands and then clears, so it must not survive into the
	// merged document. See mergeService and extends.go.
	exempt := map[string]bool{"Extends": true}
	got := reflect.ValueOf(mergeService(nil, ServiceDocument{}, override))
	for _, name := range zeroFields(got, exempt) {
		t.Errorf("mergeService drops %s: set on the override, zero after merging onto an empty base", name)
	}
}
