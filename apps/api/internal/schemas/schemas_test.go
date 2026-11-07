package schemas

import (
	"testing"

	"wise/apps/api/internal/apperr"
)

func TestValidate(t *testing.T) {
	schema := []byte(`{"type":"object","required":["request"],"properties":{"request":{"type":"string","minLength":3}},"additionalProperties":false}`)
	if err := Validate(schema, []byte(`{"request":"build it"}`), "input"); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	err := Validate(schema, []byte(`{"request":"x","extra":1}`), "input")
	ae, ok := apperr.As(err)
	if !ok || len(ae.Fields) != 2 {
		t.Fatalf("expected 2 field errors, got %v", err)
	}
	for _, f := range ae.Fields {
		if f.Message == "" {
			t.Fatalf("empty message: %+v", f)
		}
	}
	if err := Validate([]byte(`{"type":`), []byte(`{}`), "input"); err == nil {
		t.Fatal("bad schema should error")
	}
}
