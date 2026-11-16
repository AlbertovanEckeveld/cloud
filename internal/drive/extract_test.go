package drive

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range files {
		f, _ := w.Create(name)
		_, _ = f.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// minimalPDF builds a valid two-page PDF with the given texts.
func minimalPDF(texts ...string) []byte {
	var objs []string
	kids := ""
	objs = append(objs, "<< /Type /Catalog /Pages 2 0 R >>", "")
	for i, text := range texts {
		page, content := 3+2*i, 4+2*i
		kids += fmt.Sprintf("%d 0 R ", page)
		stream := fmt.Sprintf("BT /F1 24 Tf 72 720 Td (%s) Tj ET", text)
		objs = append(objs,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>", 3+2*len(texts), content),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))
	}
	objs[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, len(texts))
	objs = append(objs, "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	var b strings.Builder
	b.WriteString("%PDF-1.4\n")
	var offsets []int
	for i, o := range objs {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return []byte(b.String())
}

func TestExtractText(t *testing.T) {
	s := &Service{}
	ctx := context.Background()
	for name, tc := range map[string]struct {
		file string
		data []byte
		want []string
	}{
		"markdown": {"notities.md", []byte("# Boodschappen\nmelk en brood"), []string{"Boodschappen", "melk en brood"}},
		"docx": {"plan.docx", zipOf(t, map[string]string{"word/document.xml": `<w:document xmlns:w="w"><w:body>
			<w:p><w:r><w:t>Jaar</w:t></w:r><w:r><w:t>plan</w:t></w:r></w:p><w:p><w:r><w:t>begroting 2027</w:t></w:r></w:p></w:body></w:document>`}),
			[]string{"Jaarplan\n", "begroting 2027"}},
		"xlsx": {"cijfers.xlsx", zipOf(t, map[string]string{"xl/sharedStrings.xml": `<sst><si><t>omzet</t></si><si><t>kwartaal</t></si></sst>`}),
			[]string{"omzet", "kwartaal"}},
		"odt": {"brief.odt", zipOf(t, map[string]string{"content.xml": `<office:document-content xmlns:office="o" xmlns:text="t">
			<office:body><text:p>Beste buurman</text:p></office:body></office:document-content>`}), []string{"Beste buurman"}},
		"pdf": {"rapport.pdf", minimalPDF("Pagina een", "Pagina twee"), []string{"Pagina een", "Pagina twee"}},
	} {
		got, err := s.extractText(ctx, tc.file, tc.data)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q lacks %q", name, got, w)
			}
		}
	}
	if _, err := s.extractText(ctx, "foto.jpg", []byte{0xff, 0xd8}); err != errNotIndexable {
		t.Errorf("image: %v", err)
	}
	if _, err := s.extractText(ctx, "kapot.pdf", []byte("%PDF-1.4 rommel")); err == nil {
		t.Error("broken pdf accepted")
	}
}

func TestZipBombIsRefused(t *testing.T) {
	// 300 MiB of zeros compress to a few hundred KiB.
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	f, _ := w.Create("word/document.xml")
	chunk := make([]byte, 1<<20)
	for range 300 {
		_, _ = f.Write(chunk)
	}
	_ = w.Close()
	if _, err := officeText(buf.Bytes(), officeParts["docx"]); err == nil {
		t.Error("zip bomb unpacked")
	}
}

func TestClean(t *testing.T) {
	got := clean("a\x00b\x02c\x03d\nok\xff")
	if strings.ContainsAny(got, "\x00\x02\x03") || !strings.Contains(got, "\nok") {
		t.Errorf("clean = %q", got)
	}
	if long := clean(strings.Repeat("é", maxText)); len(long) > maxText {
		t.Errorf("not truncated: %d", len(long))
	}
}
