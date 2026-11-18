package drive

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/text/encoding/charmap"
)

// Readers for the file types beyond plain text, Office and PDF. All of them
// read untrusted input within the same limits as the rest of extraction.

// plainNames are files without an extension that are text by convention.
var plainNames = map[string]bool{
	"readme": true, "license": true, "licence": true, "copying": true, "authors": true, "notice": true,
	"changelog": true, "makefile": true, "dockerfile": true, "containerfile": true, "procfile": true,
}

// secretFiles are never indexed: keys, certificates, password databases and
// environment files hold secrets that must not end up in a search index or
// at an AI provider (ADR 0023), even for their owner.
var secretFiles = map[string]bool{
	"env": true, "pem": true, "key": true, "p12": true, "pfx": true, "jks": true, "keystore": true,
	"kdbx": true, "kdb": true, "gpg": true, "pgp": true, "ovpn": true, "ppk": true,
}

// markupFiles are read as HTML: the text without tags, scripts or styles.
var markupFiles = map[string]bool{"html": true, "htm": true, "xhtml": true}

// xmlFiles are read as XML: the character data.
var xmlFiles = map[string]bool{"xml": true, "svg": true, "gpx": true, "kml": true, "rss": true, "atom": true, "xsd": true, "xsl": true, "xslt": true}

// decodeText turns file bytes into UTF-8: it honours a byte order mark
// (UTF-8, UTF-16) and reads text that is not valid UTF-8 as Windows-1252,
// which is what older programs and Excel on Windows write.
func decodeText(data []byte) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		data = data[3:]
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}), bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		big := data[0] == 0xFE
		data = data[2:]
		units := make([]uint16, len(data)/2)
		for i := range units {
			if big {
				units[i] = uint16(data[2*i])<<8 | uint16(data[2*i+1])
			} else {
				units[i] = uint16(data[2*i+1])<<8 | uint16(data[2*i])
			}
		}
		return string(utf16.Decode(units))
	}
	if utf8.Valid(data) {
		return string(data)
	}
	if out, err := charmap.Windows1252.NewDecoder().Bytes(data); err == nil {
		return string(out)
	}
	return string(data)
}

// htmlBlocks end a line of text.
var htmlBlocks = map[string]bool{
	"p": true, "div": true, "br": true, "li": true, "tr": true, "h1": true, "h2": true, "h3": true, "h4": true,
	"h5": true, "h6": true, "section": true, "article": true, "header": true, "footer": true, "blockquote": true,
	"pre": true, "table": true, "ul": true, "ol": true, "dt": true, "dd": true, "title": true,
}

// htmlText returns the visible text of an HTML document. Scripts, styles
// and templates are skipped; nothing is executed or fetched.
func htmlText(src string, out *strings.Builder) {
	z := html.NewTokenizer(strings.NewReader(src))
	skip := 0
	for out.Len() <= maxText {
		switch z.Next() {
		case html.ErrorToken:
			return
		case html.TextToken:
			if skip == 0 {
				out.Write(z.Text())
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "noscript", "template", "head":
				if string(name) != "head" {
					skip++
				}
			case "td", "th":
				out.WriteByte('\t')
			default:
				if htmlBlocks[string(name)] {
					out.WriteByte('\n')
				}
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "script", "style", "noscript", "template":
				if skip > 0 {
					skip--
				}
			default:
				if htmlBlocks[string(name)] {
					out.WriteByte('\n')
				}
			}
		}
	}
}

// rtfText strips RTF to its text: control words go, \par becomes a line
// break, \'hh and \uN become characters, and groups that hold no text
// (fonts, colours, pictures, metadata) are skipped.
func rtfText(src string) string {
	var out strings.Builder
	skipDepth := -1 // depth of the group being skipped, -1 for none
	depth := 0
	ignorable := map[string]bool{"fonttbl": true, "colortbl": true, "stylesheet": true, "info": true, "pict": true,
		"object": true, "header": true, "footer": true, "themedata": true, "datastore": true, "listtable": true, "rsidtbl": true}
	skipNext := 0 // characters after \uN that stand in for it
	for i := 0; i < len(src) && out.Len() <= maxText; i++ {
		c := src[i]
		switch c {
		case '{':
			depth++
		case '}':
			if depth == skipDepth {
				skipDepth = -1
			}
			depth--
		case '\\':
			if i+1 >= len(src) {
				break
			}
			next := src[i+1]
			switch {
			case next == '\\' || next == '{' || next == '}':
				if skipDepth < 0 {
					out.WriteByte(next)
				}
				i++
			case next == '\'' && i+3 < len(src):
				if b, err := strconv.ParseUint(src[i+2:i+4], 16, 8); err == nil && skipDepth < 0 {
					if skipNext > 0 {
						skipNext--
					} else if r, err := charmap.Windows1252.NewDecoder().Bytes([]byte{byte(b)}); err == nil {
						out.Write(r)
					}
				}
				i += 3
			case next == '*':
				if skipDepth < 0 {
					skipDepth = depth
				}
				i++
			default:
				j := i + 1
				for j < len(src) && (src[j] >= 'a' && src[j] <= 'z' || src[j] >= 'A' && src[j] <= 'Z') {
					j++
				}
				word := src[i+1 : j]
				k := j
				if k < len(src) && (src[k] == '-' || src[k] >= '0' && src[k] <= '9') {
					k++
					for k < len(src) && src[k] >= '0' && src[k] <= '9' {
						k++
					}
				}
				param := src[j:k]
				if k < len(src) && src[k] == ' ' {
					k++ // the space ending a control word is part of it
				}
				i = k - 1
				if ignorable[word] && skipDepth < 0 {
					skipDepth = depth
				}
				if skipDepth >= 0 {
					break
				}
				switch word {
				case "par", "line", "row", "sect", "page":
					out.WriteByte('\n')
				case "tab", "cell":
					out.WriteByte('\t')
				case "u":
					if n, err := strconv.Atoi(param); err == nil {
						if n < 0 {
							n += 65536
						}
						out.WriteRune(rune(n))
						skipNext = 1
					}
				}
			}
		case '\r', '\n':
		default:
			if skipDepth >= 0 {
				break
			}
			if skipNext > 0 {
				skipNext--
				break
			}
			out.WriteByte(c)
		}
	}
	return out.String()
}

// emailText reads an e-mail (.eml): who, when and what about, its text
// (plain preferred, HTML otherwise) and the names of its attachments.
func emailText(data []byte) (string, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("read e-mail: %w", err)
	}
	var out strings.Builder
	dec := &mime.WordDecoder{CharsetReader: charsetReader}
	for _, h := range []struct{ key, label string }{{"Subject", "Onderwerp"}, {"From", "Van"}, {"To", "Aan"}, {"Cc", "Cc"}, {"Date", "Datum"}} {
		if v := msg.Header.Get(h.key); v != "" {
			if decoded, err := dec.DecodeHeader(v); err == nil {
				v = decoded
			}
			fmt.Fprintf(&out, "%s: %s\n", h.label, v)
		}
	}
	out.WriteByte('\n')
	var attachments []string
	err = mimePart(map[string][]string(msg.Header), msg.Body, 0, &out, &attachments)
	if len(attachments) > 0 {
		out.WriteString("\nBijlagen: " + strings.Join(attachments, ", ") + "\n")
	}
	return out.String(), err
}

// mimePart writes the text of one MIME part, walking into multiparts.
func mimePart(header map[string][]string, body io.Reader, depth int, out *strings.Builder, attachments *[]string) error {
	get := func(k string) string {
		if v := header[k]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	ctype, params, err := mime.ParseMediaType(get("Content-Type"))
	if err != nil {
		ctype, params = "text/plain", map[string]string{}
	}
	if _, dp, err := mime.ParseMediaType(get("Content-Disposition")); err == nil && dp["filename"] != "" {
		*attachments = append(*attachments, dp["filename"])
		return nil
	}
	switch strings.ToLower(get("Content-Transfer-Encoding")) {
	case "quoted-printable":
		body = quotedprintable.NewReader(body)
	case "base64":
		body = base64Reader(body)
	}
	if strings.HasPrefix(ctype, "multipart/") {
		if depth >= 5 {
			return nil
		}
		mr := multipart.NewReader(body, params["boundary"])
		var plain, rich strings.Builder
		for n := 0; n < 100; n++ {
			p, err := mr.NextRawPart()
			if err != nil {
				break
			}
			var part strings.Builder
			if err := mimePart(p.Header, io.LimitReader(p, maxUnpacked), depth+1, &part, attachments); err != nil {
				return err
			}
			pt, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
			if pt == "text/html" {
				rich.WriteString(part.String())
			} else {
				plain.WriteString(part.String())
			}
		}
		// multipart/alternative holds the same text twice: take one.
		if ctype == "multipart/alternative" && plain.Len() > 0 {
			out.WriteString(plain.String())
		} else {
			out.WriteString(plain.String())
			out.WriteString(rich.String())
		}
		return nil
	}
	if !strings.HasPrefix(ctype, "text/") {
		if name := params["name"]; name != "" {
			*attachments = append(*attachments, name)
		}
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(body, maxUnpacked))
	if err != nil {
		return fmt.Errorf("read e-mail part: %w", err)
	}
	text := decodeCharset(raw, params["charset"])
	if ctype == "text/html" {
		htmlText(text, out)
	} else {
		out.WriteString(text)
	}
	out.WriteByte('\n')
	return nil
}

// base64Reader decodes a base64 body; mail wraps it over lines.
func base64Reader(r io.Reader) io.Reader {
	return base64.NewDecoder(base64.StdEncoding, &lineSkipper{r: r})
}

// lineSkipper drops CR and LF, which base64 in mail contains.
type lineSkipper struct{ r io.Reader }

func (l *lineSkipper) Read(p []byte) (int, error) {
	for {
		n, err := l.r.Read(p)
		j := 0
		for _, b := range p[:n] {
			if b != '\r' && b != '\n' {
				p[j] = b
				j++
			}
		}
		if j > 0 || err != nil {
			return j, err
		}
	}
}

// decodeCharset converts the common mail charsets to UTF-8.
func decodeCharset(raw []byte, charset string) string {
	switch strings.ToLower(charset) {
	case "iso-8859-1", "latin1", "iso-8859-15", "windows-1252", "cp1252":
		if out, err := charmap.Windows1252.NewDecoder().Bytes(raw); err == nil {
			return string(out)
		}
	}
	return decodeText(raw)
}

func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	raw, err := io.ReadAll(io.LimitReader(input, 1<<20))
	if err != nil {
		return nil, err
	}
	return strings.NewReader(decodeCharset(raw, charset)), nil
}

// xlsxText reads a spreadsheet's cells, sheet by sheet: text, numbers and
// formula results, one row per line with tabs between cells.
func xlsxText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open spreadsheet: %w", err)
	}
	budget := int64(maxUnpacked)
	open := func(f *zip.File) (io.ReadCloser, *io.LimitedReader, error) {
		rc, err := f.Open()
		if err != nil {
			return nil, nil, err
		}
		return rc, &io.LimitedReader{R: rc, N: budget}, nil
	}
	var shared []string
	var sheets []*zip.File
	for _, f := range zr.File {
		switch {
		case f.Name == "xl/sharedStrings.xml":
			rc, lr, err := open(f)
			if err != nil {
				return "", err
			}
			shared, err = sharedStrings(lr)
			rc.Close()
			budget = lr.N
			if err != nil {
				return "", err
			}
		case strings.HasPrefix(f.Name, "xl/worksheets/sheet") && strings.HasSuffix(f.Name, ".xml"):
			sheets = append(sheets, f)
		}
	}
	sort.Slice(sheets, func(i, j int) bool { return slideOrder(sheets[i].Name) < slideOrder(sheets[j].Name) })
	var out strings.Builder
	for _, f := range sheets {
		rc, lr, err := open(f)
		if err != nil {
			return "", err
		}
		err = sheetText(lr, shared, &out)
		rc.Close()
		budget = lr.N
		if err != nil {
			return "", err
		}
		if budget <= 0 {
			return "", errors.New("spreadsheet unpacks to too much data")
		}
		if out.Len() > maxText {
			break
		}
		out.WriteByte('\n')
	}
	if strings.TrimSpace(out.String()) == "" {
		// No readable sheets: at least the texts the workbook holds.
		return strings.Join(shared, "\n"), nil
	}
	return out.String(), nil
}

// sharedStrings reads the strings that cells refer to by number.
func sharedStrings(r io.Reader) ([]string, error) {
	d := xml.NewDecoder(r)
	d.Strict = false
	var out []string
	var cur strings.Builder
	in := false
	for len(out) < 1_000_000 {
		tok, err := d.Token()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("read spreadsheet strings: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "si" {
				in = true
				cur.Reset()
			}
		case xml.CharData:
			if in {
				cur.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == "si" {
				out = append(out, cur.String())
				in = false
			}
		}
	}
	return out, nil
}

// sheetText writes a worksheet's cell values.
func sheetText(r io.Reader, shared []string, out *strings.Builder) error {
	d := xml.NewDecoder(r)
	d.Strict = false
	var kind string
	var value strings.Builder
	inValue := false
	for out.Len() <= maxText {
		tok, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read worksheet: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "c":
				kind = ""
				for _, a := range t.Attr {
					if a.Name.Local == "t" {
						kind = a.Value
					}
				}
				value.Reset()
			case "v", "t":
				inValue = true
			}
		case xml.CharData:
			if inValue {
				value.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "t":
				inValue = false
			case "c":
				v := value.String()
				if kind == "s" {
					if i, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && i >= 0 && i < len(shared) {
						v = shared[i]
					}
				}
				if v != "" {
					out.WriteString(v)
					out.WriteByte('\t')
				}
			case "row":
				out.WriteByte('\n')
			}
		}
	}
	return nil
}

// epubText reads an e-book: the text of its XHTML chapters in name order.
func epubText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open e-book: %w", err)
	}
	var chapters []*zip.File
	for _, f := range zr.File {
		switch strings.ToLower(path.Ext(f.Name)) {
		case ".xhtml", ".html", ".htm":
			chapters = append(chapters, f)
		}
	}
	sort.Slice(chapters, func(i, j int) bool { return chapters[i].Name < chapters[j].Name })
	var out strings.Builder
	budget := int64(maxUnpacked)
	for _, f := range chapters {
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		b, err := io.ReadAll(io.LimitReader(rc, budget))
		rc.Close()
		if err != nil {
			return "", err
		}
		budget -= int64(len(b))
		if budget <= 0 {
			return "", errors.New("e-book unpacks to too much data")
		}
		htmlText(decodeText(b), &out)
		if out.Len() > maxText {
			break
		}
	}
	return out.String(), nil
}

// zipNames lists what an archive contains, so "which zip has the
// contract" can be found; the files inside are not opened.
func zipNames(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("open archive: %w", err)
	}
	var out strings.Builder
	for i, f := range zr.File {
		if i >= 5000 || out.Len() > maxText {
			break
		}
		out.WriteString(f.Name)
		out.WriteByte('\n')
	}
	return out.String(), nil
}
