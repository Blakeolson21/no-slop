package cli

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Blakeolson21/no-slop/internal/config"
)

// The drift report is what tells a run that the head worktree set a gate
// control differently from the base ref. Its field list was written out by
// hand, which is the shape that forgets the next field somebody adds: a new
// gate-strength value would have been read from the base ref correctly and
// then silently never reported as drifted, so a contributor loosening it would
// not have been named.
//
// These tests are the completeness proof. The first fails when config.Slop and
// config.SlopRaw stop mirroring each other, which is what the reflection walk
// depends on. The second sets every leaf field in turn and requires the drift
// report to name it, which is what the walk is for.

// slopLeafPaths enumerates the yaml path of every comparable leaf in a resolved
// config struct, alongside the reflect index chain that reaches it.
func slopLeafPaths(prefix string, resolved reflect.Type, raw reflect.Type, chain []int) map[string][]int {
	leaves := make(map[string][]int)
	for index := 0; index < resolved.NumField(); index++ {
		field := resolved.Field(index)
		if !field.IsExported() {
			continue
		}
		name, rawField := configFieldName(raw, field.Name)
		path := prefix + "." + name
		next := append(append([]int(nil), chain...), index)
		if field.Type.Kind() == reflect.Struct {
			nestedRaw := reflect.TypeOf(struct{}{})
			if rawField != nil {
				nestedRaw = derefType(rawField.Type)
			}
			for nestedPath, nestedChain := range slopLeafPaths(path, field.Type, nestedRaw, next) {
				leaves[nestedPath] = nestedChain
			}
			continue
		}
		leaves[path] = next
	}
	return leaves
}

func TestSlopResolvedAndRawConfigsMirrorEachOther(t *testing.T) {
	t.Parallel()

	assertMirrors(t, "slop", reflect.TypeOf(config.Slop{}), reflect.TypeOf(config.SlopRaw{}))
}

func assertMirrors(t *testing.T, path string, resolved, raw reflect.Type) {
	t.Helper()
	rawNames := make(map[string]reflect.StructField, raw.NumField())
	for index := 0; index < raw.NumField(); index++ {
		field := raw.Field(index)
		if field.IsExported() {
			rawNames[field.Name] = field
		}
	}
	for index := 0; index < resolved.NumField(); index++ {
		field := resolved.Field(index)
		if !field.IsExported() {
			continue
		}
		rawField, ok := rawNames[field.Name]
		if !ok {
			t.Errorf("%s.%s exists in the resolved config but not in the raw one; the drift report derives its yaml names from the raw struct, so the mirror has to hold", path, field.Name)
			continue
		}
		delete(rawNames, field.Name)
		if field.Type.Kind() == reflect.Struct {
			assertMirrors(t, path+"."+field.Name, field.Type, derefType(rawField.Type))
		}
	}
	for name := range rawNames {
		t.Errorf("%s.%s exists in the raw config but not in the resolved one, so nothing compares it", path, name)
	}
}

func TestSlopConfigDriftComparesEveryConfiguredField(t *testing.T) {
	t.Parallel()

	leaves := slopLeafPaths("slop", reflect.TypeOf(config.Slop{}), reflect.TypeOf(config.SlopRaw{}), nil)
	if len(leaves) == 0 {
		t.Fatal("the resolved slop config has no comparable fields, which cannot be right")
	}

	for path, chain := range leaves {
		t.Run(path, func(t *testing.T) {
			base := config.Slop{}
			head := config.Slop{}
			target := reflect.ValueOf(&head).Elem()
			for _, index := range chain {
				target = target.Field(index)
			}
			if !setDriftedValue(target) {
				t.Fatalf("%s has type %s, which this test does not know how to drift; teach it that type so the field stays covered", path, target.Type())
			}

			drift := slopConfigDrift(head, base)
			named := false
			for _, line := range drift {
				if strings.HasPrefix(line, path+" is ") {
					named = true
				}
			}
			if !named {
				t.Fatalf("changing %s produced no drift line naming it: %v", path, drift)
			}
			if len(slopConfigDrift(base, base)) != 0 {
				t.Fatalf("identical configs reported drift: %v", slopConfigDrift(base, base))
			}
		})
	}
}

// setDriftedValue writes a value distinguishable from the zero one. An
// unhandled type fails the test rather than being skipped, so adding a field of
// a new shape is a visible decision.
func setDriftedValue(target reflect.Value) bool {
	switch target.Kind() {
	case reflect.String:
		target.SetString("drifted")
		return true
	case reflect.Bool:
		target.SetBool(true)
		return true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		target.SetInt(4242)
		return true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		target.SetUint(4242)
		return true
	case reflect.Float32, reflect.Float64:
		target.SetFloat(42.5)
		return true
	case reflect.Slice:
		drifted := reflect.MakeSlice(target.Type(), 1, 1)
		if !setDriftedValue(drifted.Index(0)) {
			return false
		}
		target.Set(drifted)
		return true
	default:
		return false
	}
}
