// Package output renders API payloads as JSON, YAML, tables, CSV/TSV, bare
// identifiers, or a Go template.
package output

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Get resolves a dotted path against a decoded JSON document.
//
//	"name"            field
//	"owner.email"     nested field
//	"data.0.uuid"     array index
//	"tags[].name"     every element of an array, collected into a slice
func Get(v any, path string) any {
	if path == "" {
		return v
	}
	return resolve(v, strings.Split(path, "."))
}

func resolve(v any, parts []string) any {
	if v == nil || len(parts) == 0 {
		return v
	}
	part := parts[0]

	if strings.HasSuffix(part, "[]") {
		container := v
		if key := strings.TrimSuffix(part, "[]"); key != "" {
			container = index(v, key)
		}
		list, ok := container.([]any)
		if !ok {
			return nil
		}
		out := make([]any, 0, len(list))
		for _, item := range list {
			if r := resolve(item, parts[1:]); r != nil {
				out = append(out, r)
			}
		}
		return out
	}

	return resolve(index(v, part), parts[1:])
}

func index(v any, key string) any {
	switch typed := v.(type) {
	case map[string]any:
		return typed[key]
	case []any:
		i, err := strconv.Atoi(key)
		if err != nil || i < 0 || i >= len(typed) {
			return nil
		}
		return typed[i]
	}
	return nil
}

// Stringify renders a resolved value as a single cell.
func Stringify(v any) string {
	switch typed := v.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case json.Number:
		return typed.String()
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			parts = append(parts, Stringify(item))
		}
		return strings.Join(parts, ", ")
	case map[string]any:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return fmt.Sprint(typed)
		}
		return string(encoded)
	default:
		return fmt.Sprint(typed)
	}
}
