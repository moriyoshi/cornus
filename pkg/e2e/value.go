package e2e

import (
	"fmt"

	"go.starlark.net/starlark"
)

// strDict converts a Go string map to a Starlark dict.
func strDict(m map[string]string) *starlark.Dict {
	d := starlark.NewDict(len(m))
	for k, v := range m {
		_ = d.SetKey(starlark.String(k), starlark.String(v))
	}
	return d
}

// anyDict converts a heterogeneous Go map to a Starlark dict.
func anyDict(m map[string]any) *starlark.Dict {
	d := starlark.NewDict(len(m))
	for k, v := range m {
		_ = d.SetKey(starlark.String(k), toStar(v))
	}
	return d
}

// toStar converts common Go values to Starlark values.
func toStar(v any) starlark.Value {
	switch t := v.(type) {
	case nil:
		return starlark.None
	case string:
		return starlark.String(t)
	case bool:
		return starlark.Bool(t)
	case int:
		return starlark.MakeInt(t)
	case int64:
		return starlark.MakeInt64(t)
	case float64:
		return starlark.Float(t)
	case []string:
		elems := make([]starlark.Value, len(t))
		for i, s := range t {
			elems[i] = starlark.String(s)
		}
		return starlark.NewList(elems)
	case []any:
		elems := make([]starlark.Value, len(t))
		for i, e := range t {
			elems[i] = toStar(e)
		}
		return starlark.NewList(elems)
	case map[string]string:
		return strDict(t)
	case map[string]any:
		return anyDict(t)
	default:
		return starlark.String(fmt.Sprintf("%v", t))
	}
}

// anyMap converts a Starlark dict to a map[string]any (scalar values), accepting
// None as nil. Used for the optional `extra` payload of bench_record.
func anyMap(v starlark.Value) (map[string]any, error) {
	if v == nil || v == starlark.None {
		return nil, nil
	}
	d, ok := v.(*starlark.Dict)
	if !ok {
		return nil, fmt.Errorf("expected a dict, got %s", v.Type())
	}
	out := make(map[string]any, d.Len())
	for _, item := range d.Items() {
		k, ok := starlark.AsString(item[0])
		if !ok {
			return nil, fmt.Errorf("dict keys must be strings")
		}
		out[k] = fromStar(item[1])
	}
	return out, nil
}

// fromStar converts a scalar Starlark value to a Go value (best-effort; the
// inverse of toStar for the scalar cases). Non-scalars fall back to their
// Starlark string form.
func fromStar(v starlark.Value) any {
	switch t := v.(type) {
	case starlark.String:
		return string(t)
	case starlark.Bool:
		return bool(t)
	case starlark.Int:
		if i, ok := t.Int64(); ok {
			return i
		}
		return t.String()
	case starlark.Float:
		return float64(t)
	default:
		return v.String()
	}
}

// strSlice extracts a []string from a Starlark iterable (list/tuple), accepting
// None as empty.
func strSlice(v starlark.Value) ([]string, error) {
	if v == nil || v == starlark.None {
		return nil, nil
	}
	iter, ok := v.(starlark.Iterable)
	if !ok {
		return nil, fmt.Errorf("expected a list, got %s", v.Type())
	}
	var out []string
	it := iter.Iterate()
	defer it.Done()
	var x starlark.Value
	for it.Next(&x) {
		s, ok := starlark.AsString(x)
		if !ok {
			return nil, fmt.Errorf("expected string elements, got %s", x.Type())
		}
		out = append(out, s)
	}
	return out, nil
}

// strOrList accepts either a bare string (a single value) or a list/tuple of
// strings, returning a []string. A bare Starlark string must NOT be treated as
// an iterable here (it would yield individual characters), so it is special-cased.
func strOrList(v starlark.Value) ([]string, error) {
	if v == nil || v == starlark.None {
		return nil, nil
	}
	if s, ok := v.(starlark.String); ok {
		return []string{string(s)}, nil
	}
	return strSlice(v)
}

// strMap extracts a map[string]string from a Starlark dict, accepting None.
func strMap(v starlark.Value) (map[string]string, error) {
	if v == nil || v == starlark.None {
		return nil, nil
	}
	d, ok := v.(*starlark.Dict)
	if !ok {
		return nil, fmt.Errorf("expected a dict, got %s", v.Type())
	}
	out := make(map[string]string, d.Len())
	for _, item := range d.Items() {
		k, ok := starlark.AsString(item[0])
		if !ok {
			return nil, fmt.Errorf("dict keys must be strings")
		}
		val, ok := starlark.AsString(item[1])
		if !ok {
			return nil, fmt.Errorf("dict value for %q must be a string", k)
		}
		out[k] = val
	}
	return out, nil
}
