package sso

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// ExclusiveC14NAlgorithm is the URI identifying Exclusive XML Canonicalization
// (without comments). It is the canonicalization method UBAG applies to the
// <SignedInfo> and signed element before SAML signature verification.
const ExclusiveC14NAlgorithm = "http://www.w3.org/2001/10/xml-exc-c14n#"

// exclusiveCanonicalize renders the first XML element found in input (and all
// of its descendants) into Exclusive XML Canonicalization form
// (http://www.w3.org/2001/10/xml-exc-c14n#, "without comments"), per
// https://www.w3.org/TR/xml-exc-c14n/ .
//
// Scope and documented limitations (this is a focused, stdlib-only
// implementation; the gateway intentionally avoids pulling in a heavy XML
// security dependency):
//
//   - Input is parsed with encoding/xml, which resolves namespaces to URIs.
//     Original prefixes are reconstructed from the xmlns declarations carried by
//     the canonicalized subtree itself. Each namespace URI is assumed to map to
//     a single stable prefix within that subtree (true for SAML assertions
//     emitted by mainstream IdPs, which declare xmlns:saml / xmlns:ds on the
//     signed element or the <Signature>).
//   - The signed subtree MUST carry the namespace declarations it uses.
//     Namespaces inherited solely from ancestors outside the canonicalized
//     element are not reconstructed; encountering such a prefix fails closed.
//   - The InclusiveNamespaces PrefixList extension is NOT supported.
//   - DTD-defaulted attributes are NOT synthesised (UBAG rejects DTDs).
//   - Comments and processing instructions are dropped.
//
// These limitations fail closed for signature verification: signing and
// verification both run this exact function, so any construct it cannot
// represent yields a different (still deterministic) octet stream, which makes
// the signature fail to verify rather than falsely pass.
func exclusiveCanonicalize(input []byte) ([]byte, error) {
	decoder := xml.NewDecoder(bytes.NewReader(input))

	var out bytes.Buffer

	// inScope is a stack of (prefix -> uri) maps, one per open element,
	// recording the namespace declarations each element added to the in-scope
	// context. It is used to reconstruct prefixes from resolved URIs.
	var inScope []map[string]string

	// rendered tracks the (prefix -> uri) declarations already emitted into the
	// output by ancestors so that exclusive canonicalization only emits a
	// namespace where it is first visibly utilized.
	rendered := map[string]string{}

	type restoreEntry struct {
		prefix string
		had    bool
		old    string
	}
	var restoreStack [][]restoreEntry

	lookupURI := func(prefix string) (string, bool) {
		for i := len(inScope) - 1; i >= 0; i-- {
			if uri, ok := inScope[i][prefix]; ok {
				return uri, true
			}
		}
		return "", false
	}
	prefixForURI := func(uri string) (string, bool) {
		for i := len(inScope) - 1; i >= 0; i-- {
			for prefix, declared := range inScope[i] {
				if declared == uri {
					return prefix, true
				}
			}
		}
		return "", false
	}

	depth := 0
	started := false

	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}

		switch element := token.(type) {
		case xml.StartElement:
			started = true
			depth++

			// Collect this element's own namespace declarations.
			decls := map[string]string{}
			for _, attr := range element.Attr {
				switch {
				case attr.Name.Space == "xmlns":
					decls[attr.Name.Local] = attr.Value
				case attr.Name.Space == "" && attr.Name.Local == "xmlns":
					decls[""] = attr.Value
				}
			}
			inScope = append(inScope, decls)

			// Resolve element prefix from its namespace URI.
			elemPrefix := ""
			if element.Name.Space != "" {
				prefix, ok := prefixForURI(element.Name.Space)
				if !ok {
					return nil, fmt.Errorf("exc-c14n: no prefix in scope for element namespace %q", element.Name.Space)
				}
				elemPrefix = prefix
			}
			elemName := element.Name.Local
			if elemPrefix != "" {
				elemName = elemPrefix + ":" + element.Name.Local
			}

			type canonAttr struct {
				prefix string
				uri    string
				local  string
				value  string
			}
			var attrs []canonAttr
			utilized := map[string]bool{}

			if element.Name.Space != "" {
				utilized[elemPrefix] = true
			} else if current, ok := rendered[""]; ok && current != "" {
				// Element is in no namespace but an ancestor emitted a default
				// namespace; it must be undeclared with xmlns="".
				utilized[""] = true
			}

			for _, attr := range element.Attr {
				if attr.Name.Space == "xmlns" || (attr.Name.Space == "" && attr.Name.Local == "xmlns") {
					continue
				}
				attrPrefix := ""
				if attr.Name.Space != "" {
					prefix, ok := prefixForURI(attr.Name.Space)
					if !ok {
						return nil, fmt.Errorf("exc-c14n: no prefix in scope for attribute namespace %q", attr.Name.Space)
					}
					attrPrefix = prefix
					utilized[attrPrefix] = true
				}
				attrs = append(attrs, canonAttr{prefix: attrPrefix, uri: attr.Name.Space, local: attr.Name.Local, value: attr.Value})
			}

			type emittedNS struct {
				prefix string
				uri    string
			}
			var nsToEmit []emittedNS
			var restores []restoreEntry
			for prefix := range utilized {
				var uri string
				if prefix == "" && element.Name.Space == "" {
					uri = "" // undeclare the inherited default namespace
				} else {
					resolved, ok := lookupURI(prefix)
					if !ok {
						return nil, fmt.Errorf("exc-c14n: no namespace in scope for prefix %q", prefix)
					}
					uri = resolved
				}
				if current, ok := rendered[prefix]; !ok || current != uri {
					restores = append(restores, restoreEntry{prefix: prefix, had: ok, old: current})
					rendered[prefix] = uri
					nsToEmit = append(nsToEmit, emittedNS{prefix: prefix, uri: uri})
				}
			}
			restoreStack = append(restoreStack, restores)

			sort.Slice(nsToEmit, func(i, j int) bool {
				return nsToEmit[i].prefix < nsToEmit[j].prefix
			})
			sort.Slice(attrs, func(i, j int) bool {
				if attrs[i].uri != attrs[j].uri {
					return attrs[i].uri < attrs[j].uri
				}
				return attrs[i].local < attrs[j].local
			})

			out.WriteByte('<')
			out.WriteString(elemName)
			for _, ns := range nsToEmit {
				if ns.prefix == "" {
					out.WriteString(` xmlns="`)
				} else {
					out.WriteString(" xmlns:")
					out.WriteString(ns.prefix)
					out.WriteString(`="`)
				}
				out.WriteString(escapeC14NAttr(ns.uri))
				out.WriteByte('"')
			}
			for _, attr := range attrs {
				out.WriteByte(' ')
				if attr.prefix != "" {
					out.WriteString(attr.prefix)
					out.WriteByte(':')
				}
				out.WriteString(attr.local)
				out.WriteString(`="`)
				out.WriteString(escapeC14NAttr(attr.value))
				out.WriteByte('"')
			}
			out.WriteByte('>')

		case xml.EndElement:
			elemPrefix := ""
			if element.Name.Space != "" {
				if prefix, ok := prefixForURI(element.Name.Space); ok {
					elemPrefix = prefix
				}
			}
			elemName := element.Name.Local
			if elemPrefix != "" {
				elemName = elemPrefix + ":" + element.Name.Local
			}
			out.WriteString("</")
			out.WriteString(elemName)
			out.WriteByte('>')

			restores := restoreStack[len(restoreStack)-1]
			restoreStack = restoreStack[:len(restoreStack)-1]
			for _, entry := range restores {
				if entry.had {
					rendered[entry.prefix] = entry.old
				} else {
					delete(rendered, entry.prefix)
				}
			}
			inScope = inScope[:len(inScope)-1]

			depth--
			if started && depth == 0 {
				return out.Bytes(), nil
			}

		case xml.CharData:
			if depth > 0 {
				out.WriteString(escapeC14NText(string(element)))
			}

		case xml.Comment, xml.ProcInst, xml.Directive:
			// Dropped: exclusive canonicalization without comments.
		}
	}

	if !started {
		return nil, errors.New("exc-c14n: no XML element found in input")
	}
	return out.Bytes(), nil
}

// escapeC14NText escapes character data per the canonicalization rules: only
// &, <, > and carriage return are escaped; all other characters (including
// quotes and other whitespace) are preserved verbatim.
func escapeC14NText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '\r':
			b.WriteString("&#xD;")
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

// escapeC14NAttr escapes attribute values per the canonicalization rules: &,
// <, " and the tab / newline / carriage-return control characters are escaped.
func escapeC14NAttr(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '"':
			b.WriteString("&quot;")
		case '\t':
			b.WriteString("&#x9;")
		case '\n':
			b.WriteString("&#xA;")
		case '\r':
			b.WriteString("&#xD;")
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
