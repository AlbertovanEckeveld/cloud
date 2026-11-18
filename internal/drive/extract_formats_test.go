package drive

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
)

func utf16le(s string) []byte {
	out := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(s)) {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}

func TestExtractMoreFormats(t *testing.T) {
	s := &Service{}
	ctx := context.Background()
	xlsx := zipOf(t, map[string]string{
		"xl/sharedStrings.xml":     `<sst><si><t>Post</t></si><si><t>Licenties</t></si><si><r><t>Ser</t></r><r><t>vers</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row><c t="s"><v>0</v></c><c><v>Bedrag</v></c></row><row><c t="s"><v>1</v></c><c><v>1200.5</v></c></row><row><c t="s"><v>2</v></c><c t="inlineStr"><is><t>op aanvraag</t></is></c></row></sheetData></worksheet>`,
	})
	docx := zipOf(t, map[string]string{
		"word/document.xml":  `<w:document><w:body><w:p><w:r><w:t>Hoofdtekst</w:t></w:r></w:p></w:body></w:document>`,
		"word/header1.xml":   `<w:hdr><w:p><w:r><w:t>Vertrouwelijk</w:t></w:r></w:p></w:hdr>`,
		"word/footnotes.xml": `<w:footnotes><w:footnote><w:p><w:r><w:t>Bron: jaarverslag</w:t></w:r></w:p></w:footnote></w:footnotes>`,
	})
	pptx := zipOf(t, map[string]string{
		"ppt/slides/slide1.xml":           `<p:sld><a:p><a:r><a:t>Roadmap</a:t></a:r></a:p></p:sld>`,
		"ppt/notesSlides/notesSlide1.xml": `<p:notes><a:p><a:r><a:t>Spreek over Q3</a:t></a:r></a:p></p:notes>`,
	})
	epub := zipOf(t, map[string]string{
		"OEBPS/ch1.xhtml": `<html><head><title>x</title><style>p{}</style></head><body><h1>Hoofdstuk één</h1><p>Het begin.</p></body></html>`,
		"mimetype":        "application/epub+zip",
	})
	eml := "From: =?UTF-8?Q?Jan_B=C3=B6ker?= <jan@example.test>\r\nTo: ann@example.test\r\nSubject: Offerte dakkapel\r\n" +
		"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b1\"\r\n\r\n" +
		"--b1\r\nContent-Type: multipart/alternative; boundary=\"b2\"\r\n\r\n" +
		"--b2\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nPrijs: 5000 euro, ge=EFnteresseerd?\r\n" +
		"--b2\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Prijs: 5000 euro</p>\r\n--b2--\r\n" +
		"--b1\r\nContent-Type: application/pdf; name=\"offerte.pdf\"\r\nContent-Disposition: attachment; filename=\"offerte.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0x\r\n--b1--\r\n"

	cases := []struct {
		name string
		data []byte
		want []string
		not  []string
	}{
		{"kosten.csv", utf16le("Post;Bedrag\nLicenties;1200"), []string{"Licenties;1200"}, nil},
		{"oud.csv", []byte("Caf\xe9;Kosten\n"), []string{"Café;Kosten"}, nil}, // Windows-1252, as Excel writes it
		{"pagina.html", []byte(`<html><head><script>alert("x")</script><style>.a{}</style></head><body><h1>Titel</h1><p>Tekst &amp; meer</p></body></html>`),
			[]string{"Titel", "Tekst & meer"}, []string{"alert", ".a{}"}},
		{"kaart.svg", []byte(`<svg><text>Plattegrond</text></svg>`), []string{"Plattegrond"}, []string{"<text>"}},
		{"brief.rtf", []byte(`{\rtf1\ansi{\fonttbl{\f0 Arial;}}{\colortbl;\red0;}\f0 Beste Ann,\par Groeten van caf\'e9 \u8364? Jan}`),
			[]string{"Beste Ann,", "café", "€", "Jan"}, []string{"Arial", "fonttbl", "red0"}},
		{"mail.eml", []byte(eml), []string{"Onderwerp: Offerte dakkapel", "Jan Böker", "Prijs: 5000 euro, geïnteresseerd?", "Bijlagen: offerte.pdf"}, []string{"JVBERi0x"}},
		{"begroting.xlsx", xlsx, []string{"Post", "Licenties\t1200.5", "Servers\top aanvraag"}, nil},
		{"rapport.docx", docx, []string{"Hoofdtekst", "Vertrouwelijk", "Bron: jaarverslag"}, nil},
		{"pitch.pptx", pptx, []string{"Roadmap", "Spreek over Q3"}, nil},
		{"boek.epub", epub, []string{"Hoofdstuk één", "Het begin."}, []string{"p{}"}},
		{"archief.zip", zipOf(t, map[string]string{"contracten/huur-2025.pdf": "x"}), []string{"contracten/huur-2025.pdf"}, nil},
		{"README", []byte("Zo installeer je het"), []string{"Zo installeer je het"}, nil},
		{"main.kt", []byte("fun main() = println(\"hoi\")"), []string{"println"}, nil},
	}
	for _, c := range cases {
		text, err := s.extractText(ctx, c.name, c.data)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s: missing %q in %q", c.name, w, text)
			}
		}
		for _, n := range c.not {
			if strings.Contains(text, n) {
				t.Errorf("%s: should not contain %q: %q", c.name, n, text)
			}
		}
	}
}

func TestSecretsAreNeverIndexed(t *testing.T) {
	s := &Service{}
	for _, name := range []string{".env", ".env.local", "server.key", "cert.pem", "wachtwoorden.kdbx", "id.p12", "vpn.ovpn"} {
		if _, err := s.extractText(context.Background(), name, []byte("SECRET=hunter2")); !errors.Is(err, errNotIndexable) {
			t.Errorf("%s was indexed: %v", name, err)
		}
	}
}
