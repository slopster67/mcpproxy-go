package servericon

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/draw"
	"image/png"
)

// Normalize trims fully transparent PNG borders once, when artwork is imported.
// It preserves the artwork's aspect ratio and leaves SVGs and unpadded PNGs as-is.
func Normalize(icon string) (string, error) {
	if err := Validate(icon); err != nil {
		return "", err
	}
	if icon == "" || MIMEType(icon) != "image/png" {
		return icon, nil
	}
	data, err := Bytes(icon)
	if err != nil {
		return "", err
	}
	source, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	full := source.Bounds()
	visible := image.Rectangle{}
	for y := full.Min.Y; y < full.Max.Y; y++ {
		for x := full.Min.X; x < full.Max.X; x++ {
			if _, _, _, alpha := source.At(x, y).RGBA(); alpha != 0 {
				visible = visible.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	// Empty images retain a valid canvas; never produce a zero-sized PNG.
	if visible.Empty() || visible == full {
		return icon, nil
	}
	cropped := image.NewNRGBA(image.Rect(0, 0, visible.Dx(), visible.Dy()))
	draw.Draw(cropped, cropped.Bounds(), source, visible.Min, draw.Src)
	var output bytes.Buffer
	if err := png.Encode(&output, cropped); err != nil {
		return "", err
	}
	normalized := pngPrefix + base64.StdEncoding.EncodeToString(output.Bytes())
	if err := Validate(normalized); err != nil {
		return "", err
	}
	return normalized, nil
}
