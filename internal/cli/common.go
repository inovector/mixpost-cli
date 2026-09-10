package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/inovector/mixpost-cli/internal/mixpost"
	"github.com/inovector/mixpost-cli/internal/output"
	"github.com/spf13/cobra"
)

// queryValues is the query string of a request.
type queryValues = url.Values

// listFlags are the pagination flags shared by every list command.
type listFlags struct {
	page    int
	perPage int
	all     bool
	limit   int

	// filter, when set, is applied to the fetched records before printing.
	// It exists for the qualities the API has no query parameter for.
	filter func(map[string]any) bool
}

func (l *listFlags) register(cmd *cobra.Command) {
	f := cmd.Flags()
	f.IntVar(&l.page, "page", 0, "page to fetch")
	f.IntVar(&l.perPage, "per-page", 0, "items per page")
	f.BoolVar(&l.all, "all", false, "fetch every page")
	f.IntVar(&l.limit, "limit", 0, "stop after this many items (implies --all)")
}

func (l *listFlags) query() url.Values {
	q := url.Values{}
	if l.page > 0 {
		q.Set("page", strconv.Itoa(l.page))
	}
	if l.perPage > 0 {
		q.Set("per_page", strconv.Itoa(l.perPage))
	}
	return q
}

// run fetches one page, or walks the whole collection when --all or --limit
// was given, and prints the result.
func (l *listFlags) run(cmd *cobra.Command, e *Env, path string, extra url.Values, columns []output.Column) error {
	q := l.query()
	for k, values := range extra {
		for _, v := range values {
			q.Add(k, v)
		}
	}

	if l.all || l.limit > 0 {
		raw, err := e.Client.Paginate(ctxOf(cmd), path, q, l.limit)
		if err != nil {
			return err
		}
		return e.renderFiltered(raw, l.filter, columns)
	}

	resp, err := e.Client.Get(ctxOf(cmd), path, q)
	if err != nil {
		return err
	}
	if resp.Status == 0 || l.filter == nil {
		return e.Render(resp, columns)
	}
	return e.renderFiltered(resp.Body, l.filter, columns)
}

func (e *Env) renderFiltered(raw []byte, keep func(map[string]any) bool, columns []output.Column) error {
	if keep == nil || e.DryRun {
		return e.RenderRaw(raw, columns)
	}
	filtered, err := filterRows(raw, keep)
	if err != nil {
		return err
	}
	return e.RenderRaw(filtered, columns)
}

// readInput reads a JSON document from a file, or from stdin when the path is
// "-".
func readInput(path string) ([]byte, error) {
	if path == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// readJSONObject loads a JSON object from a file or stdin.
func readJSONObject(path string) (map[string]any, error) {
	data, err := readInput(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", describeInput(path), err)
	}
	return doc, nil
}

func describeInput(path string) string {
	if path == "-" {
		return "stdin"
	}
	return path
}

// parseIDs turns repeated and comma-separated flag values into integers.
func parseIDs(values []string, flagName string) ([]int, error) {
	var ids []int
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			id, err := strconv.Atoi(part)
			if err != nil {
				return nil, usageError(fmt.Errorf("--%s expects numeric ids, got %q", flagName, part))
			}
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// splitList flattens repeated and comma-separated flag values.
func splitList(values []string) []string {
	var out []string
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// parseKeyValue splits "key=value" and rejects anything else.
func parseKeyValue(pair, flagName string) (string, string, error) {
	key, value, found := strings.Cut(pair, "=")
	if !found || strings.TrimSpace(key) == "" {
		return "", "", usageError(fmt.Errorf("--%s expects key=value, got %q", flagName, pair))
	}
	return strings.TrimSpace(key), value, nil
}

// inferValue types a command-line value the way a JSON document would:
// booleans, null and numbers stay scalars, @file loads a JSON document, and a
// value that parses as a JSON array or object is kept as such.
func inferValue(raw string) (any, error) {
	trimmed := strings.TrimSpace(raw)
	switch trimmed {
	case "true":
		return true, nil
	case "false":
		return false, nil
	case "null":
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "@") {
		data, err := readInput(strings.TrimPrefix(trimmed, "@"))
		if err != nil {
			return nil, err
		}
		var doc any
		if err := json.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("parse %s: %w", describeInput(strings.TrimPrefix(trimmed, "@")), err)
		}
		return doc, nil
	}
	if i, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
		return i, nil
	}
	if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
		return f, nil
	}
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		var doc any
		if err := json.Unmarshal([]byte(trimmed), &doc); err == nil {
			return doc, nil
		}
	}
	return raw, nil
}

// setNested writes a value at a dotted path, creating intermediate maps.
func setNested(doc map[string]any, path string, value any) error {
	parts := strings.Split(path, ".")
	current := doc
	for i, part := range parts[:len(parts)-1] {
		next, ok := current[part]
		if !ok || next == nil {
			created := map[string]any{}
			current[part] = created
			current = created
			continue
		}
		asMap, ok := next.(map[string]any)
		if !ok {
			return usageError(fmt.Errorf("cannot set %q: %q is not an object", path, strings.Join(parts[:i+1], ".")))
		}
		current = asMap
	}
	current[parts[len(parts)-1]] = value
	return nil
}

// requireFlags fails when a required flag was left out.
func requireFlags(cmd *cobra.Command, names ...string) error {
	var missing []string
	for _, name := range names {
		if flag := cmd.Flags().Lookup(name); flag == nil || !flag.Changed {
			missing = append(missing, "--"+name)
		}
	}
	if len(missing) > 0 {
		return usageError(fmt.Errorf("missing required %s: %s", plural(len(missing), "flag", "flags"), strings.Join(missing, ", ")))
	}
	return nil
}

func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return singular
	}
	return pluralForm
}

// deleteMany sends a DELETE with a body, which is how the API takes a list of
// identifiers rather than a single one in the path.
func deleteMany(ctx context.Context, e *Env, path string, body map[string]any) error {
	resp, err := e.Client.Call(ctx, "DELETE", path, nil, body)
	if err != nil {
		return err
	}
	return e.Render(resp, nil)
}

// exactArgs is cobra.ExactArgs with a message that shows the right invocation.
func exactArgs(n int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return usageError(fmt.Errorf("expected %d %s: %s", n, plural(n, "argument", "arguments"), usage))
		}
		return nil
	}
}

// minArgs is cobra.MinimumNArgs with a message that shows the right invocation.
func minArgs(n int, usage string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < n {
			return usageError(fmt.Errorf("expected at least %d %s: %s", n, plural(n, "argument", "arguments"), usage))
		}
		return nil
	}
}

// marshalCollection wraps rows in the {"data": [...]} envelope the printer and
// the rest of the API both use.
func marshalCollection[T any](rows []T) ([]byte, error) {
	if rows == nil {
		rows = []T{}
	}
	return json.Marshal(map[string]any{"data": rows})
}

// filterRows applies a client-side predicate to a collection payload, leaving
// the envelope and every field of the kept records intact.
func filterRows(raw []byte, keep func(map[string]any) bool) ([]byte, error) {
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("the response was not JSON: %w", err)
	}

	rows := output.Rows(doc)
	kept := make([]any, 0, len(rows))
	for _, row := range rows {
		record, ok := row.(map[string]any)
		if !ok || keep(record) {
			kept = append(kept, row)
		}
	}

	envelope, ok := doc.(map[string]any)
	if !ok {
		return json.Marshal(map[string]any{"data": kept})
	}
	if _, hasData := envelope["data"]; !hasData {
		return json.Marshal(map[string]any{"data": kept})
	}
	envelope["data"] = kept
	if meta, ok := envelope["meta"].(map[string]any); ok {
		meta["filtered"] = len(kept)
	}
	return json.Marshal(envelope)
}

// toInt reads an identifier that the API may encode as a number or a string.
func toInt(v any) (int, bool) {
	switch typed := v.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case json.Number:
		n, err := typed.Int64()
		return int(n), err == nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(typed))
		return n, err == nil
	}
	return 0, false
}

// asMap is a nil-safe type assertion used when walking decoded payloads.
func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

// asSlice is a nil-safe type assertion used when walking decoded payloads.
func asSlice(v any) ([]any, bool) {
	s, ok := v.([]any)
	return s, ok
}

func parseFloat(s string) (float64, error) {
	return strconv.ParseFloat(strings.TrimSpace(s), 64)
}

// instanceRequest addresses an endpoint that sits outside any workspace.
func instanceRequest(method, path string) mixpost.Request {
	return mixpost.Request{Method: method, Path: path, Instance: true}
}
