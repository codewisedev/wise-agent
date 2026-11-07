// Package schemas compiles and applies JSON Schemas (draft 2020-12).
package schemas

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"

	"wise/apps/api/internal/apperr"
)

var printer = message.NewPrinter(language.English)

var (
	mu    sync.Mutex
	cache = map[string]*jsonschema.Schema{}
)

// Compile compiles raw (cached by content).
func Compile(raw []byte) (*jsonschema.Schema, error) {
	sum := sha256.Sum256(raw)
	key := hex.EncodeToString(sum[:])
	mu.Lock()
	defer mu.Unlock()
	if s, ok := cache[key]; ok {
		return s, nil
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	url := "mem://" + key + ".json"
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	s, err := c.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("compile schema: %w", err)
	}
	cache[key] = s
	return s, nil
}

// Validate checks instance (JSON) against schema (JSON) and returns an
// apperr validation error listing the failing locations.
func Validate(schema, instance []byte, field string) error {
	s, err := Compile(schema)
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(instance))
	if err != nil {
		return apperr.Validation(apperr.FieldError{Field: field, Message: "must be valid JSON"})
	}
	if err := s.Validate(inst); err != nil {
		var ve *jsonschema.ValidationError
		if errors.As(err, &ve) {
			return apperr.Validation(flatten(ve, field)...)
		}
		return apperr.Validation(apperr.FieldError{Field: field, Message: err.Error()})
	}
	return nil
}

func flatten(ve *jsonschema.ValidationError, field string) []apperr.FieldError {
	var out []apperr.FieldError
	var walk func(e *jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			loc := field
			if len(e.InstanceLocation) > 0 {
				loc = field + "." + strings.Join(e.InstanceLocation, ".")
			}
			out = append(out, apperr.FieldError{Field: loc, Message: e.ErrorKind.LocalizedString(printer)})
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(ve)
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}
