package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// errMapNotEditable surfaces when the user targets a map-typed schema field
// (e.g. execution.lock_keys) through `config get`/`config set`. The flat
// string format cannot round-trip a map without an escape grammar, so we
// route the user to `marunage config edit` instead of leaking reflect terms.
var errMapNotEditable = errors.New("map fields are not editable via 'config get'/'config set'; use 'marunage config edit' instead")

// Get returns the value at the dotted-path key as a flat string, suitable
// for `marunage config get` to print to stdout. Slice values are joined
// with commas to match the inverse format Set accepts.
func Get(c Config, key string) (string, error) {
	v, err := resolveField(reflect.ValueOf(c), key)
	if err != nil {
		return "", err
	}
	s, err := formatScalar(v)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	return s, nil
}

// Set parses value into the type of the field at key, runs Validate, and
// only mutates *c when both succeed. Setting execution.permission_mode also
// rewrites execution.claude_command for the four canonical modes so the
// derived command never drifts from the user's chosen mode.
func Set(c *Config, key, value string) error {
	candidate := *c
	field, err := resolveField(reflect.ValueOf(&candidate).Elem(), key)
	if err != nil {
		return err
	}
	if err := assignFromString(field, value); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}

	// Spec: permission_mode drives claude_command for non-custom modes.
	if key == "execution.permission_mode" {
		if cmd := ClaudeCommandFor(value); cmd != "" {
			candidate.Execution.ClaudeCommand = cmd
		}
	}

	// Validate's errors already start with the field name, so do not wrap
	// them with `key:` here — that produced "foo.bar: foo.bar: ...".
	if err := candidate.Validate(); err != nil {
		return err
	}
	*c = candidate
	return nil
}

// resolveField walks the dotted-path key by matching each segment against
// the `toml` tag of the current struct's fields. It returns a settable
// reflect.Value pointing at the leaf field.
func resolveField(v reflect.Value, key string) (reflect.Value, error) {
	parts := strings.Split(key, ".")
	current := v
	for i, part := range parts {
		if current.Kind() != reflect.Struct {
			return reflect.Value{}, fmt.Errorf("%s: %s is not a struct table", key, strings.Join(parts[:i], "."))
		}
		next, ok := fieldByTomlTag(current, part)
		if !ok {
			return reflect.Value{}, fmt.Errorf("%s: unknown key", key)
		}
		current = next
	}
	return current, nil
}

func fieldByTomlTag(v reflect.Value, tag string) (reflect.Value, bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		ft := t.Field(i)
		// "toml:\"name,omitempty\"" — the part before the comma is the name.
		raw := ft.Tag.Get("toml")
		name := raw
		if idx := strings.Index(raw, ","); idx >= 0 {
			name = raw[:idx]
		}
		if name == tag {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

func formatScalar(v reflect.Value) (string, error) {
	switch v.Kind() {
	case reflect.String:
		return v.String(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), nil
	case reflect.Bool:
		return strconv.FormatBool(v.Bool()), nil
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'f', -1, 64), nil
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return "", fmt.Errorf("unsupported slice element type %s", v.Type().Elem().Kind())
		}
		parts := make([]string, v.Len())
		for i := 0; i < v.Len(); i++ {
			parts[i] = v.Index(i).String()
		}
		return strings.Join(parts, ","), nil
	case reflect.Map:
		return "", errMapNotEditable
	}
	return "", fmt.Errorf("unsupported config value type %s", v.Kind())
}

func assignFromString(v reflect.Value, raw string) error {
	if !v.CanSet() {
		return fmt.Errorf("field is not settable")
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(raw)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("expected integer: %w", err)
		}
		v.SetInt(n)
		return nil
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("expected boolean (true/false): %w", err)
		}
		v.SetBool(b)
		return nil
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("expected number: %w", err)
		}
		v.SetFloat(f)
		return nil
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("unsupported slice element type %s", v.Type().Elem().Kind())
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			v.Set(reflect.MakeSlice(v.Type(), 0, 0))
			return nil
		}
		// JSON array input: parse as JSON to preserve commas inside elements.
		if strings.HasPrefix(raw, "[") {
			var elems []string
			if err := json.Unmarshal([]byte(raw), &elems); err != nil {
				return fmt.Errorf("expected JSON array of strings: %w", err)
			}
			out := reflect.MakeSlice(v.Type(), len(elems), len(elems))
			for i, e := range elems {
				out.Index(i).SetString(e)
			}
			v.Set(out)
			return nil
		}
		// CSV fallback: comma-separated list, whitespace trimmed.
		parts := strings.Split(raw, ",")
		out := reflect.MakeSlice(v.Type(), len(parts), len(parts))
		for i, p := range parts {
			out.Index(i).SetString(strings.TrimSpace(p))
		}
		v.Set(out)
		return nil
	case reflect.Map:
		return errMapNotEditable
	}
	return fmt.Errorf("unsupported config value type %s", v.Kind())
}
