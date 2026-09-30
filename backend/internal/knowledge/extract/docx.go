package extract

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
)

const maxDocumentXML = 64 << 20

// headingStyle matches paragraph style ids/names used for headings in
// English and Mongolian Word templates ("Heading1", "heading 2", "Title",
// "Гарчиг1").
var headingStyle = regexp.MustCompile(`(?i)^(heading|title|гарчиг)\s*([1-9])?$`)

// docxText reads word/document.xml: paragraphs become lines, heading
// paragraphs "# Heading" lines, list items "- item", and table rows
// "cell | cell" lines.
func docxText(data []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", invalid(err, "The DOCX file is damaged or not a Word document.")
	}
	var doc *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			doc = f
			break
		}
	}
	if doc == nil {
		return "", invalid(nil, "The DOCX file has no document body.")
	}
	rc, err := doc.Open()
	if err != nil {
		return "", invalid(err, "The DOCX file is damaged.")
	}
	defer rc.Close()
	text, err := parseDocumentXML(io.LimitReader(rc, maxDocumentXML))
	if err != nil {
		return "", invalid(err, "The DOCX file is damaged.")
	}
	return text, nil
}

type docxParser struct {
	out strings.Builder

	para       strings.Builder
	inPara     bool
	heading    int // 0 = body text
	listItem   bool
	tableDepth int
	row        []string
	cell       []string
}

func parseDocumentXML(r io.Reader) (string, error) {
	dec := xml.NewDecoder(r)
	dec.Strict = false
	p := &docxParser{}
	inText := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				p.inPara, p.heading, p.listItem = true, 0, false
				p.para.Reset()
			case "pStyle":
				if m := headingStyle.FindStringSubmatch(strings.TrimSpace(attr(t, "val"))); m != nil {
					p.heading = 1
					if m[2] != "" {
						p.heading, _ = strconv.Atoi(m[2])
					}
				}
			case "outlineLvl":
				if lvl, err := strconv.Atoi(attr(t, "val")); err == nil && lvl >= 0 && lvl < 9 && p.heading == 0 {
					p.heading = lvl + 1
				}
			case "numPr":
				p.listItem = true
			case "t":
				inText = true
			case "tab":
				p.para.WriteString("\t")
			case "br", "cr":
				p.para.WriteString(" ")
			case "tbl":
				p.tableDepth++
				p.flushLine("")
			case "tr":
				if p.tableDepth == 1 {
					p.row = p.row[:0]
				}
			case "tc":
				if p.tableDepth == 1 {
					p.cell = p.cell[:0]
				}
			}
		case xml.CharData:
			if inText && p.inPara {
				p.para.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				p.endParagraph()
			case "tc":
				if p.tableDepth == 1 {
					p.row = append(p.row, strings.Join(p.cell, " "))
				}
			case "tr":
				if p.tableDepth == 1 {
					p.endRow()
				}
			case "tbl":
				p.tableDepth--
			}
		}
	}
	return p.out.String(), nil
}

func (p *docxParser) endParagraph() {
	text := strings.Join(strings.Fields(p.para.String()), " ")
	p.inPara = false
	if text == "" {
		return
	}
	if p.tableDepth > 0 {
		p.cell = append(p.cell, text)
		return
	}
	switch {
	case p.heading > 0:
		level := min(p.heading, 6)
		p.flushLine(strings.Repeat("#", level) + " " + text)
	case p.listItem:
		p.flushLine("- " + text)
	default:
		p.flushLine(text)
	}
}

func (p *docxParser) endRow() {
	cells := make([]string, 0, len(p.row))
	for _, c := range p.row {
		if c = strings.TrimSpace(c); c != "" {
			cells = append(cells, c)
		}
	}
	if len(cells) > 0 {
		p.out.WriteString(strings.Join(cells, " | "))
		p.out.WriteString("\n\n")
	}
}

// flushLine writes line as its own paragraph (blank-line separated).
func (p *docxParser) flushLine(line string) {
	if line == "" {
		return
	}
	p.out.WriteString(line)
	p.out.WriteString("\n\n")
}

func attr(e xml.StartElement, local string) string {
	for _, a := range e.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}
