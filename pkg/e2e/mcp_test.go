package e2e

import (
	"reflect"
	"testing"

	"go.starlark.net/starlark"
)

func TestFromStarlarkJSONNested(t *testing.T) {
	args := starlark.NewDict(2)
	_ = args.SetKey(starlark.String("name"), starlark.String("demo"))
	_ = args.SetKey(starlark.String("cmd"), starlark.NewList([]starlark.Value{
		starlark.String("sh"),
		starlark.String("-c"),
		starlark.String("exit 7"),
	}))

	got, err := fromStarlarkJSON(args)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"name": "demo",
		"cmd":  []any{"sh", "-c", "exit 7"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fromStarlarkJSON: got %#v, want %#v", got, want)
	}
}

func TestFromStarlarkJSONRejectsNonStringDictKey(t *testing.T) {
	args := starlark.NewDict(1)
	_ = args.SetKey(starlark.MakeInt(1), starlark.String("value"))
	if _, err := fromStarlarkJSON(args); err == nil {
		t.Fatal("fromStarlarkJSON accepted a non-string dict key")
	}
}

func TestDecodeMCPJSONPreservesIntegralNumbers(t *testing.T) {
	got := decodeMCPJSON(`{"exitCode":7,"ratio":1.5}`).(map[string]any)
	if got["exitCode"] != int64(7) {
		t.Fatalf("exitCode: got %#v (%T), want int64(7)", got["exitCode"], got["exitCode"])
	}
	if got["ratio"] != float64(1.5) {
		t.Fatalf("ratio: got %#v (%T), want float64(1.5)", got["ratio"], got["ratio"])
	}
}

func TestDecodeMCPJSONNonJSONIsNil(t *testing.T) {
	if got := decodeMCPJSON("plain text"); got != nil {
		t.Fatalf("decodeMCPJSON: got %#v, want nil", got)
	}
}
