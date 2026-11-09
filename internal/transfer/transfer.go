// Package transfer handles portable Lighthouse exports. SQLite remains the
// canonical database; these formats are interchange documents, not schemas.
package transfer

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/paulwritescode/lighthouse/internal/store"
	"gopkg.in/yaml.v3"
)

type Dataset struct {
	Version   int              `json:"version" yaml:"version"`
	Artifacts []store.Artifact `json:"artifacts" yaml:"artifacts"`
	Memories  []store.Memory   `json:"memories" yaml:"memories"`
}

func Export(format string, artifacts []store.Artifact, memories []store.Memory) ([]byte, string, string, error) {
	d := Dataset{Version: 1, Artifacts: artifacts, Memories: memories}
	switch strings.ToLower(format) {
	case "json":
		b, e := json.MarshalIndent(d, "", "  ")
		return b, "application/json", "json", e
	case "yaml", "yml":
		b, e := yaml.Marshal(d)
		return b, "application/yaml", "yaml", e
	case "toml":
		b, e := marshalTOML(d)
		return b, "application/toml", "toml", e
	case "md", "markdown":
		b, e := marshalMarkdown(d)
		return b, "text/markdown", "md", e
	case "pdf":
		b, e := marshalPDF(d)
		return b, "application/pdf", "pdf", e
	default:
		return nil, "", "", fmt.Errorf("unsupported export format %q (use json, md, yaml, toml, pdf, or db)", format)
	}
}

func Import(format string, raw []byte) (Dataset, error) {
	var d Dataset
	switch strings.ToLower(format) {
	case "json":
		if err := json.Unmarshal(raw, &d); err != nil {
			return d, err
		}
	case "yaml", "yml":
		if err := yaml.Unmarshal(raw, &d); err != nil {
			return d, err
		}
	case "toml":
		return unmarshalTOML(raw)
	case "md", "markdown":
		return unmarshalMarkdown(raw)
	case "pdf":
		return importPDF(raw)
	default:
		return d, fmt.Errorf("unsupported import format %q (use json, md, yaml, toml, pdf, or db)", format)
	}
	if d.Version > 1 {
		return d, fmt.Errorf("export version %d is newer than this Lighthouse build supports", d.Version)
	}
	return d, nil
}

func marshalMarkdown(d Dataset) ([]byte, error) {
	data, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("# Lighthouse data export\n\n")
	fmt.Fprintf(&b, "Records: %d results · %d memory items\n\n", len(d.Artifacts), len(d.Memories))
	b.WriteString("<!-- lighthouse-json: " + base64.StdEncoding.EncodeToString(data) + " -->\n\n")
	for _, a := range d.Artifacts {
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", a.Title, a.Content)
	}
	return []byte(b.String()), nil
}
func unmarshalMarkdown(raw []byte) (Dataset, error) {
	var d Dataset
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "<!-- lighthouse-json: ") && strings.HasSuffix(line, " -->") {
			encoded := strings.TrimSuffix(strings.TrimPrefix(line, "<!-- lighthouse-json: "), " -->")
			b, e := base64.StdEncoding.DecodeString(encoded)
			if e != nil {
				return d, e
			}
			if e = json.Unmarshal(b, &d); e != nil {
				return d, e
			}
			return d, nil
		}
	}
	return d, errors.New("Markdown is readable/exportable, but import requires a Lighthouse export block; use JSON, YAML, or TOML for general data interchange")
}

func marshalTOML(d Dataset) ([]byte, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "version = %d\n", d.Version)
	for _, a := range d.Artifacts {
		x, e := json.Marshal(a)
		if e != nil {
			return nil, e
		}
		fmt.Fprintf(&b, "\n[[artifacts]]\nrecord_json = %s\n", strconv.Quote(string(x)))
	}
	for _, m := range d.Memories {
		x, e := json.Marshal(m)
		if e != nil {
			return nil, e
		}
		fmt.Fprintf(&b, "\n[[memories]]\nrecord_json = %s\n", strconv.Quote(string(x)))
	}
	return []byte(b.String()), nil
}
func unmarshalTOML(raw []byte) (Dataset, error) {
	var d Dataset
	section := ""
	record := ""
	flush := func() error {
		if record == "" {
			return nil
		}
		switch section {
		case "artifacts":
			var a store.Artifact
			if e := json.Unmarshal([]byte(record), &a); e != nil {
				return e
			}
			d.Artifacts = append(d.Artifacts, a)
		case "memories":
			var m store.Memory
			if e := json.Unmarshal([]byte(record), &m); e != nil {
				return e
			}
			d.Memories = append(d.Memories, m)
		}
		record = ""
		return nil
	}
	for _, rawline := range strings.Split(string(raw), "\n") {
		line := strings.TrimSpace(rawline)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]] ") {
			continue
		}
		if strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]") {
			if e := flush(); e != nil {
				return d, e
			}
			section = strings.TrimSuffix(strings.TrimPrefix(line, "[["), "]]")
			if section != "artifacts" && section != "memories" {
				return d, fmt.Errorf("unsupported TOML section %q", section)
			}
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return d, fmt.Errorf("invalid TOML line %q", line)
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		if key == "version" {
			d.Version, _ = strconv.Atoi(value)
		} else if key == "record_json" {
			v, e := strconv.Unquote(value)
			if e != nil {
				return d, e
			}
			record = v
		}
	}
	if e := flush(); e != nil {
		return d, e
	}
	return d, nil
}

// PDF export is a compact text report. Imported PDFs are retained as a single
// source artifact because generic PDF text extraction is outside this local
// storage binary's trusted feature set.
func marshalPDF(d Dataset) ([]byte, error) {
	var lines []string
	lines = append(lines, "Lighthouse local data export", fmt.Sprintf("%d results  |  %d memories", len(d.Artifacts), len(d.Memories)), "")
	for _, a := range d.Artifacts {
		lines = append(lines, a.Title, a.Project+"  /  "+a.Agent+"  /  "+a.Kind)
		lines = append(lines, wrap(a.Content, 88)...)
		lines = append(lines, "")
	}
	for _, m := range d.Memories {
		lines = append(lines, "Memory: "+m.Key, m.Project+"  /  "+m.Kind)
		lines = append(lines, wrap(m.Body, 88)...)
		lines = append(lines, "")
	}
	return textPDF(lines), nil
}
func importPDF(raw []byte) (Dataset, error) {
	if len(raw) > 1400<<10 {
		return Dataset{}, errors.New("PDF exceeds the 1.4 MiB attachment limit")
	}
	return Dataset{Version: 1, Artifacts: []store.Artifact{{Title: "Imported PDF document", Content: "PDF attachment imported as a source document. Lighthouse retains the encoded file; text extraction is not performed.", Kind: "imported-pdf", Agent: "import", Fields: map[string]any{"file_name": "imported.pdf", "media_type": "application/pdf", "base64": base64.StdEncoding.EncodeToString(raw)}}}}, nil
}
func wrap(s string, n int) []string {
	var out []string
	for _, p := range strings.Split(s, "\n") {
		for len([]rune(p)) > n {
			r := []rune(p)
			out = append(out, string(r[:n]))
			p = string(r[n:])
		}
		out = append(out, p)
	}
	return out
}
func textPDF(lines []string) []byte {
	var content strings.Builder
	content.WriteString("BT /F1 10 Tf 48 790 Td 13 TL\n")
	for i, line := range lines {
		if i > 0 {
			content.WriteString("T*\n")
		}
		line = pdfASCII(line)
		line = strings.ReplaceAll(line, "\\", "\\\\")
		line = strings.ReplaceAll(line, "(", "\\(")
		line = strings.ReplaceAll(line, ")", "\\)")
		fmt.Fprintf(&content, "(%s) Tj\n", line)
	}
	content.WriteString("ET")
	stream := content.String()
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "<< /Type /Pages /Kids [3 0 R] /Count 1 >>", "<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 842] /Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>", fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream)}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, o := range offsets[1:] {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer << /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return b.Bytes()
}
func pdfASCII(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 32 && r <= 126 {
			b.WriteRune(r)
		} else {
			b.WriteByte('?')
		}
	}
	return b.String()
}

func WritePDF(w io.Writer, d Dataset) error {
	b, e := marshalPDF(d)
	if e != nil {
		return e
	}
	_, e = w.Write(b)
	return e
}
