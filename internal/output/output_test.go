package output

import (
	"bytes"
	"strings"
	"testing"
)

func newTestPrinter(format Format) (*Printer, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	return &Printer{Format: format, Out: &out, Err: &errOut}, &out, &errOut
}

const postsPayload = `{"data":[
  {"uuid":"aaa","status":"draft","tags":[{"name":"launch"},{"name":"news"}],
   "accounts":[{"username":"acme"}],
   "versions":[{"content":[{"body":"<div>Hello &amp; welcome</div>"}]}]},
  {"uuid":"bbb","status":"published","tags":[],"accounts":[],
   "versions":[{"content":[{"body":"Second"}]}]}
]}`

func testColumns() []Column {
	return []Column{
		{Header: "uuid", Path: "uuid"},
		{Header: "status", Path: "status"},
		{Header: "tags", Path: "tags[].name"},
		{Header: "content", Fn: func(row any) string { return StripHTML(Get(row, "versions.0.content.0.body")) }},
	}
}

func TestTableOutput(t *testing.T) {
	printer, out, _ := newTestPrinter(FormatTable)
	if err := printer.Print([]byte(postsPayload), testColumns()); err != nil {
		t.Fatalf("Print: %v", err)
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %d, want 3 (header plus two rows):\n%s", len(lines), out.String())
	}
	if !strings.HasPrefix(lines[0], "UUID") {
		t.Errorf("header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "launch, news") {
		t.Errorf("tags were not joined: %q", lines[1])
	}
	if !strings.Contains(lines[1], "Hello & welcome") {
		t.Errorf("HTML was not flattened: %q", lines[1])
	}
}

func TestNoHeaders(t *testing.T) {
	printer, out, _ := newTestPrinter(FormatTable)
	printer.NoHeaders = true
	if err := printer.Print([]byte(postsPayload), testColumns()); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if strings.Contains(out.String(), "UUID") {
		t.Errorf("header was printed:\n%s", out.String())
	}
}

func TestIDOutputIsOneIdentifierPerLine(t *testing.T) {
	printer, out, _ := newTestPrinter(FormatID)
	if err := printer.Print([]byte(postsPayload), testColumns()); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if got := out.String(); got != "aaa\nbbb\n" {
		t.Errorf("output = %q, want %q", got, "aaa\nbbb\n")
	}
}

func TestIDOutputFallsBackToNumericID(t *testing.T) {
	printer, out, _ := newTestPrinter(FormatID)
	if err := printer.Print([]byte(`{"data":[{"id":4},{"id":9}]}`), nil); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if got := out.String(); got != "4\n9\n" {
		t.Errorf("output = %q", got)
	}
}

func TestCSVOutput(t *testing.T) {
	printer, out, _ := newTestPrinter(FormatCSV)
	if err := printer.Print([]byte(postsPayload), testColumns()); err != nil {
		t.Fatalf("Print: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if lines[0] != "uuid,status,tags,content" {
		t.Errorf("header = %q", lines[0])
	}
	if !strings.Contains(lines[1], `"launch, news"`) {
		t.Errorf("row = %q, want the joined tags quoted", lines[1])
	}
}

func TestJSONOutputIsPassedThrough(t *testing.T) {
	printer, out, _ := newTestPrinter(FormatJSON)
	if err := printer.Print([]byte(`{"uuid":"aaa","extra_field_the_cli_does_not_know":42}`), testColumns()); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if !strings.Contains(out.String(), "extra_field_the_cli_does_not_know") {
		t.Errorf("unknown fields were dropped:\n%s", out.String())
	}
}

func TestYAMLOutputKeepsNumbersUnquoted(t *testing.T) {
	printer, out, _ := newTestPrinter(FormatYAML)
	if err := printer.Print([]byte(`{"balance":35,"low":false}`), nil); err != nil {
		t.Fatalf("Print: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "balance: 35") {
		t.Errorf("output = %q", got)
	}
	if !strings.Contains(got, "low: false") {
		t.Errorf("output = %q", got)
	}
}

func TestTemplateOutput(t *testing.T) {
	printer, out, _ := newTestPrinter(FormatTemplate)
	printer.Template = `{{range .data}}{{.uuid}}={{.status}} {{end}}`
	if err := printer.Print([]byte(postsPayload), nil); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "aaa=draft bbb=published" {
		t.Errorf("output = %q", got)
	}
}

func TestTemplateWithoutTemplateFails(t *testing.T) {
	printer, _, _ := newTestPrinter(FormatTemplate)
	if err := printer.Print([]byte(`{}`), nil); err == nil {
		t.Fatal("expected an error")
	}
}

func TestEmptyCollectionPrintsNoteOnStderr(t *testing.T) {
	printer, out, errOut := newTestPrinter(FormatTable)
	if err := printer.Print([]byte(`{"data":[]}`), testColumns()); err != nil {
		t.Fatalf("Print: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want it empty", out.String())
	}
	if !strings.Contains(errOut.String(), "No items") {
		t.Errorf("stderr = %q", errOut.String())
	}
}

func TestSingleObjectIsOneRow(t *testing.T) {
	printer, out, _ := newTestPrinter(FormatTable)
	if err := printer.Print([]byte(`{"uuid":"aaa","status":"draft"}`), testColumns()); err != nil {
		t.Fatalf("Print: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("lines = %d, want 2:\n%s", len(lines), out.String())
	}
}

func TestGetPaths(t *testing.T) {
	doc := map[string]any{
		"a": map[string]any{"b": "c"},
		"list": []any{
			map[string]any{"name": "one"},
			map[string]any{"name": "two"},
		},
	}
	if got := Stringify(Get(doc, "a.b")); got != "c" {
		t.Errorf("a.b = %q", got)
	}
	if got := Stringify(Get(doc, "list.1.name")); got != "two" {
		t.Errorf("list.1.name = %q", got)
	}
	if got := Stringify(Get(doc, "list[].name")); got != "one, two" {
		t.Errorf("list[].name = %q", got)
	}
	if got := Get(doc, "missing.path"); got != nil {
		t.Errorf("missing.path = %v, want nil", got)
	}
}

func TestTruncateAddsEllipsis(t *testing.T) {
	if got := truncate("abcdefgh", 5); got != "abcd…" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("abc", 0); got != "abc" {
		t.Errorf("truncate with no limit = %q", got)
	}
}

func TestSanitizeCellKeepsColumnsAligned(t *testing.T) {
	printer, out, _ := newTestPrinter(FormatTable)
	payload := `{"data":[{"uuid":"aaa","status":"a\tb\nc"}]}`
	if err := printer.Print([]byte(payload), []Column{{Header: "uuid", Path: "uuid"}, {Header: "status", Path: "status"}}); err != nil {
		t.Fatalf("Print: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Errorf("lines = %d, want 2:\n%s", len(lines), out.String())
	}
}

func TestParseFormatRejectsUnknown(t *testing.T) {
	if _, err := ParseFormat("xml"); err == nil {
		t.Fatal("expected an error")
	}
	for _, name := range Formats {
		if _, err := ParseFormat(name); err != nil {
			t.Errorf("ParseFormat(%q): %v", name, err)
		}
	}
}
