// Package servericon handles small, self-contained SVG and PNG server icons.
package servericon

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"image/png"
	"io"
	"strings"
)

const MaxSize = 64 * 1024

const pngPrefix = "data:image/png;base64,"

func MIMEType(icon string) string {
	if strings.HasPrefix(icon, pngPrefix) {
		return "image/png"
	}
	return "image/svg+xml"
}

// Bytes returns the image file bytes used by the public icon endpoint.
func Bytes(icon string) ([]byte, error) {
	if strings.HasPrefix(icon, pngPrefix) {
		return base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(icon, pngPrefix))
	}
	return []byte(icon), nil
}

// Validate accepts small PNGs or static SVG artwork without active content or
// external resources. Bound PNG dimensions before decoding pixel data.
func Validate(icon string) error {
	if icon == "" {
		return nil
	}
	if len(icon) > len(pngPrefix)+base64.StdEncoding.EncodedLen(MaxSize) {
		return fmt.Errorf("Icon must be 64 KB or smaller")
	}
	data, err := Bytes(icon)
	if err != nil {
		return fmt.Errorf("Invalid PNG encoding")
	}
	if len(data) > MaxSize {
		return fmt.Errorf("Icon must be 64 KB or smaller")
	}
	if MIMEType(icon) == "image/png" {
		dimensions, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("Invalid PNG image")
		}
		if dimensions.Width > 1024 || dimensions.Height > 1024 {
			return fmt.Errorf("PNG must be 1024 × 1024 pixels or smaller")
		}
		if _, err := png.Decode(bytes.NewReader(data)); err != nil {
			return fmt.Errorf("Invalid PNG image")
		}
		return nil
	}
	return validateSVG(string(data))
}

func validateSVG(svg string) error {
	elements := strings.Fields("svg g defs path rect circle ellipse line polyline polygon title desc linearGradient radialGradient stop clipPath mask use symbol")
	allowed := make(map[string]bool, len(elements))
	for _, name := range elements {
		allowed[name] = true
	}
	attrs := strings.Fields("id viewBox width height x y x1 x2 y1 y2 cx cy r rx ry d points fill fill-rule fill-opacity stroke stroke-width stroke-linecap stroke-linejoin stroke-miterlimit stroke-dasharray stroke-dashoffset stroke-opacity opacity transform gradientTransform gradientUnits spreadMethod offset stop-color stop-opacity clip-path clip-rule clipPathUnits mask maskUnits maskContentUnits preserveAspectRatio href version color vector-effect role aria-hidden aria-label focusable")
	allowedAttrs := make(map[string]bool, len(attrs))
	for _, name := range attrs {
		allowedAttrs[name] = true
	}
	decoder := xml.NewDecoder(strings.NewReader(svg))
	depth, roots := 0, 0
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("Invalid SVG: %w", err)
		}
		switch t := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
				if roots != 1 || t.Name.Local != "svg" {
					return fmt.Errorf("Icon must contain one SVG root")
				}
			}
			depth++
			if !allowed[t.Name.Local] || (t.Name.Space != "" && t.Name.Space != "http://www.w3.org/2000/svg") {
				return fmt.Errorf("Unsupported SVG element: %s", t.Name.Local)
			}
			for _, a := range t.Attr {
				if a.Name.Local == "xmlns" || a.Name.Space == "xmlns" {
					continue
				}
				if !allowedAttrs[a.Name.Local] || (a.Name.Space != "" && a.Name.Space != "http://www.w3.org/1999/xlink") {
					return fmt.Errorf("Unsupported SVG attribute: %s", a.Name.Local)
				}
				value := strings.TrimSpace(a.Value)
				if a.Name.Local == "href" && !strings.HasPrefix(value, "#") {
					return fmt.Errorf("SVG references must be local")
				}
				if strings.Contains(strings.ToLower(value), "url(") && !(strings.HasPrefix(value, "url(#") && strings.HasSuffix(value, ")") && strings.Count(value, "url(") == 1) {
					return fmt.Errorf("SVG resources must be local")
				}
			}
		case xml.EndElement:
			depth--
		case xml.Directive:
			return fmt.Errorf("SVG declarations are not supported")
		case xml.ProcInst:
			if t.Target != "xml" {
				return fmt.Errorf("SVG processing instructions are not supported")
			}
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(string(t)) != "" {
				return fmt.Errorf("Unexpected content outside SVG")
			}
		}
	}
	if roots != 1 || depth != 0 {
		return fmt.Errorf("Icon must contain one complete SVG")
	}
	return nil
}

func Path(icon string) string {
	if icon == "" {
		return ""
	}
	extension := "svg"
	if MIMEType(icon) == "image/png" {
		extension = "png"
	}
	return fmt.Sprintf("/server-icons/%x.%s", sha256.Sum256([]byte(icon)), extension)
}

func DataURI(icon string) string {
	if MIMEType(icon) == "image/png" {
		return icon
	}
	return "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(icon))
}
