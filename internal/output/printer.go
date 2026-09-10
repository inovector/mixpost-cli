package output

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"text/template"

	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

// Format is a rendering mode.
type Format string

const (
	FormatJSON     Format = "json"
	FormatYAML     Format = "yaml"
	FormatTable    Format = "table"
	FormatCSV      Format = "csv"
	FormatTSV      Format = "tsv"
	FormatID       Format = "id"
	FormatTemplate Format = "template"
)

// Formats lists every accepted value of --output.
var Formats = []string{
	string(FormatJSON), string(FormatYAML), string(FormatTable),
	string(FormatCSV), string(FormatTSV), string(FormatID), string(FormatTemplate),
}

// ParseFormat validates a --output value.
func ParseFormat(s string) (Format, error) {
	switch Format(strings.ToLower(strings.TrimSpace(s))) {
	case FormatJSON:
		return FormatJSON, nil
	case FormatYAML:
		return FormatYAML, nil
	case FormatTable:
		return FormatTable, nil
	case FormatCSV:
		return FormatCSV, nil
	case FormatTSV:
		return FormatTSV, nil
	case FormatID:
		return FormatID, nil
	case FormatTemplate:
		return FormatTemplate, nil
	}
	return "", fmt.Errorf("unknown output format %q (want one of: %s)", s, strings.Join(Formats, ", "))
}

// Column describes one table, CSV or TSV column.
type Column struct {
	Header string
	Path   string
	// MaxWidth truncates the cell in table output only; 0 means no limit.
	MaxWidth int
	// Fn overrides Path with a custom extractor.
	Fn func(row any) string
}

func (c Column) value(row any) string {
	if c.Fn != nil {
		return c.Fn(row)
	}
	return Stringify(Get(row, c.Path))
}

// Printer renders payloads to a writer.
type Printer struct {
	Format    Format
	Out       io.Writer
	Err       io.Writer
	Template  string
	NoHeaders bool
	Compact   bool
}

// New builds a Printer writing to stdout and stderr.
func New(format Format) *Printer {
	return &Printer{Format: format, Out: os.Stdout, Err: os.Stderr}
}

// IsTTY reports whether f is an interactive terminal. The file mode is not
// enough on its own: /dev/null is a character device too, and treating it as a
// terminal would make a confirmation prompt hang on an unattended run.
func IsTTY(f *os.File) bool {
	if f == nil {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// DefaultFormat is a table for humans and JSON for everything else, so a
// command in a pipe or a script always emits machine-readable output.
func DefaultFormat() Format {
	if IsTTY(os.Stdout) {
		return FormatTable
	}
	return FormatJSON
}

// Print renders a raw JSON payload using the columns supplied by the command.
func (p *Printer) Print(raw []byte, columns []Column) error {
	switch p.Format {
	case FormatJSON:
		return p.printJSON(raw)
	case FormatYAML:
		return p.printYAML(raw)
	case FormatTemplate:
		return p.printTemplate(raw)
	}

	doc, err := decode(raw)
	if err != nil {
		return err
	}
	rows := Rows(doc)

	switch p.Format {
	case FormatTable:
		return p.printTable(rows, columns)
	case FormatCSV:
		return p.printSeparated(rows, columns, ',')
	case FormatTSV:
		return p.printSeparated(rows, columns, '\t')
	case FormatID:
		return p.printIDs(rows)
	}
	return fmt.Errorf("unsupported output format %q", p.Format)
}

// PrintMessage writes a human-facing note. It goes to stderr so that stdout
// stays a clean data stream for scripts.
func (p *Printer) PrintMessage(format string, args ...any) {
	fmt.Fprintf(p.Err, format+"\n", args...)
}

// Rows turns a payload into the list of records to tabulate: the "data"
// collection when there is one, the top-level array when the payload is a
// list, and otherwise the document itself as a single row.
func Rows(doc any) []any {
	switch typed := doc.(type) {
	case []any:
		return typed
	case map[string]any:
		if data, ok := typed["data"].([]any); ok {
			return data
		}
		return []any{typed}
	case nil:
		return nil
	}
	return []any{doc}
}

func decode(raw []byte) (any, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var doc any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("the response was not JSON: %w", err)
	}
	return doc, nil
}

func (p *Printer) printJSON(raw []byte) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if p.Compact {
		var buf bytes.Buffer
		if err := json.Compact(&buf, raw); err != nil {
			_, err := p.Out.Write(append(bytes.TrimRight(raw, "\n"), '\n'))
			return err
		}
		buf.WriteByte('\n')
		_, err := p.Out.Write(buf.Bytes())
		return err
	}

	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		_, err := p.Out.Write(append(bytes.TrimRight(raw, "\n"), '\n'))
		return err
	}
	buf.WriteByte('\n')
	_, err := p.Out.Write(buf.Bytes())
	return err
}

func (p *Printer) printYAML(raw []byte) error {
	doc, err := decode(raw)
	if err != nil {
		return err
	}
	if doc == nil {
		return nil
	}
	encoded, err := yaml.Marshal(normalizeNumbers(doc))
	if err != nil {
		return err
	}
	_, err = p.Out.Write(encoded)
	return err
}

// normalizeNumbers converts json.Number back to concrete types so YAML does
// not quote every numeric value.
func normalizeNumbers(v any) any {
	switch typed := v.(type) {
	case json.Number:
		if i, err := typed.Int64(); err == nil {
			return i
		}
		if f, err := typed.Float64(); err == nil {
			return f
		}
		return typed.String()
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, item := range typed {
			out[k] = normalizeNumbers(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = normalizeNumbers(item)
		}
		return out
	}
	return v
}

func (p *Printer) printTemplate(raw []byte) error {
	if strings.TrimSpace(p.Template) == "" {
		return fmt.Errorf("--output template needs --template")
	}
	doc, err := decode(raw)
	if err != nil {
		return err
	}
	tmpl, err := template.New("output").Funcs(templateFuncs()).Parse(p.Template)
	if err != nil {
		return fmt.Errorf("parse --template: %w", err)
	}
	if err := tmpl.Execute(p.Out, normalizeNumbers(doc)); err != nil {
		return fmt.Errorf("render --template: %w", err)
	}
	if !strings.HasSuffix(p.Template, "\n") {
		fmt.Fprintln(p.Out)
	}
	return nil
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"json": func(v any) (string, error) {
			encoded, err := json.Marshal(v)
			return string(encoded), err
		},
		"text": StripHTML,
		"join": func(sep string, list []any) string {
			parts := make([]string, 0, len(list))
			for _, item := range list {
				parts = append(parts, Stringify(item))
			}
			return strings.Join(parts, sep)
		},
	}
}

func (p *Printer) printTable(rows []any, columns []Column) error {
	if len(columns) == 0 {
		columns = inferColumns(rows)
	}
	if len(rows) == 0 {
		p.PrintMessage("No items.")
		return nil
	}

	tw := tabwriter.NewWriter(p.Out, 0, 8, 2, ' ', 0)
	if !p.NoHeaders {
		headers := make([]string, len(columns))
		for i, col := range columns {
			headers[i] = strings.ToUpper(col.Header)
		}
		fmt.Fprintln(tw, strings.Join(headers, "\t"))
	}
	for _, row := range rows {
		cells := make([]string, len(columns))
		for i, col := range columns {
			cells[i] = sanitizeCell(truncate(col.value(row), col.MaxWidth))
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
	}
	return tw.Flush()
}

func (p *Printer) printSeparated(rows []any, columns []Column, sep rune) error {
	if len(columns) == 0 {
		columns = inferColumns(rows)
	}
	writer := csv.NewWriter(p.Out)
	writer.Comma = sep

	if !p.NoHeaders && len(columns) > 0 {
		headers := make([]string, len(columns))
		for i, col := range columns {
			headers[i] = col.Header
		}
		if err := writer.Write(headers); err != nil {
			return err
		}
	}
	for _, row := range rows {
		cells := make([]string, len(columns))
		for i, col := range columns {
			cells[i] = col.value(row)
		}
		if err := writer.Write(cells); err != nil {
			return err
		}
	}
	writer.Flush()
	return writer.Error()
}

// printIDs writes one identifier per line: the uuid when the record has one,
// otherwise the numeric id. It is the format meant for xargs.
func (p *Printer) printIDs(rows []any) error {
	for _, row := range rows {
		id := Stringify(Get(row, "uuid"))
		if id == "" {
			id = Stringify(Get(row, "id"))
		}
		if id == "" {
			continue
		}
		if _, err := fmt.Fprintln(p.Out, id); err != nil {
			return err
		}
	}
	return nil
}

// inferColumns is the fallback for payloads a command did not describe: it
// keeps the scalar fields of the first record, in a stable order.
func inferColumns(rows []any) []Column {
	if len(rows) == 0 {
		return nil
	}
	record, ok := rows[0].(map[string]any)
	if !ok {
		return []Column{{Header: "VALUE", Fn: func(row any) string { return Stringify(row) }}}
	}

	preferred := []string{"id", "uuid", "name", "username", "email", "provider", "status", "title"}
	seen := map[string]bool{}
	var columns []Column

	for _, key := range preferred {
		if _, ok := record[key]; ok && !seen[key] {
			seen[key] = true
			columns = append(columns, Column{Header: key, Path: key, MaxWidth: 40})
		}
	}
	keys := make([]string, 0, len(record))
	for key, value := range record {
		if seen[key] {
			continue
		}
		switch value.(type) {
		case map[string]any, []any:
			continue
		}
		keys = append(keys, key)
	}
	sortStrings(keys)
	for _, key := range keys {
		columns = append(columns, Column{Header: key, Path: key, MaxWidth: 40})
	}
	return columns
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

var (
	htmlTag    = regexp.MustCompile(`<[^>]*>`)
	whitespace = regexp.MustCompile(`\s+`)
)

// StripHTML flattens the HTML fragment Mixpost stores as post content into a
// single readable line.
func StripHTML(v any) string {
	s := Stringify(v)
	s = strings.ReplaceAll(s, "<br>", " ")
	s = strings.ReplaceAll(s, "</div>", " ")
	s = htmlTag.ReplaceAllString(s, "")
	s = strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&nbsp;", " ").Replace(s)
	return strings.TrimSpace(whitespace.ReplaceAllString(s, " "))
}

func truncate(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxWidth {
		return s
	}
	if maxWidth <= 1 {
		return string(runes[:maxWidth])
	}
	return string(runes[:maxWidth-1]) + "…"
}

// sanitizeCell keeps a multi-line or tab-carrying value from breaking the
// column layout.
func sanitizeCell(s string) string {
	if !strings.ContainsAny(s, "\t\n\r") {
		return s
	}
	return strings.NewReplacer("\t", " ", "\n", " ", "\r", "").Replace(s)
}
