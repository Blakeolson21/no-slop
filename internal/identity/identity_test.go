package identity

import "testing"

func TestLookupEnvRejectsSetEmptyAliasConflict(t *testing.T) {
	t.Setenv("NS_SAMPLE", "")
	t.Setenv("NM_SAMPLE", "1")

	if _, err := LookupEnv("NS_SAMPLE", "NM_SAMPLE"); err == nil {
		t.Fatal("expected set-empty alias conflict")
	}
}

func TestLookupEnvAcceptsEqualSetEmptyAliases(t *testing.T) {
	t.Setenv("NS_SAMPLE", "")
	t.Setenv("NM_SAMPLE", "")

	got, err := LookupEnv("NS_SAMPLE", "NM_SAMPLE")
	if err != nil {
		t.Fatalf("LookupEnv returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("LookupEnv = %q, want empty", got)
	}
}

func TestLookupEnvSliceRejectsSetEmptyAliasConflict(t *testing.T) {
	env := []string{"NS_SAMPLE=", "NM_SAMPLE=1"}

	if _, err := LookupEnvSlice(env, "NS_SAMPLE", "NM_SAMPLE"); err == nil {
		t.Fatal("expected set-empty alias conflict")
	}
}

func TestLookupEnvSliceAcceptsEqualSetEmptyAliases(t *testing.T) {
	env := []string{"NS_SAMPLE=", "NM_SAMPLE="}

	got, err := LookupEnvSlice(env, "NS_SAMPLE", "NM_SAMPLE")
	if err != nil {
		t.Fatalf("LookupEnvSlice returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("LookupEnvSlice = %q, want empty", got)
	}
}
