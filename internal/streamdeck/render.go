package streamdeck

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"sync"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

// A key's image, as the sd program drew it: the picture scaled to fit the
// key less labelMargin at the bottom, centered, and the label in 14-point
// monospace, white, centered, its baseline labelBaseline from the bottom,
// all on black.
const (
	labelMargin   = 20
	labelBaseline = 5
	labelSize     = 14
)

var (
	faceOnce sync.Once
	face     font.Face
	faceErr  error
)

// labelFace is the labels' typeface: Go Mono.
func labelFace() (font.Face, error) {
	faceOnce.Do(func() {
		var f *opentype.Font
		if f, faceErr = opentype.Parse(gomono.TTF); faceErr == nil {
			face, faceErr = opentype.NewFace(f, &opentype.FaceOptions{Size: labelSize, DPI: 72, Hinting: font.HintingFull})
		}
	})
	return face, faceErr
}

// Draw makes a key's image: picture, a PNG (none for none), with label.
func Draw(picture []byte, label string) (*image.RGBA, error) {
	key := image.NewRGBA(image.Rect(0, 0, KeySize, KeySize))
	draw.Draw(key, key.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)

	if len(picture) > 0 {
		src, err := png.Decode(bytes.NewReader(picture))
		if err != nil {
			return nil, fmt.Errorf("reading the picture: %w", err)
		}
		// Scaled to fit, keeping its shape, centered in the space above
		// the label.
		boxW, boxH := KeySize, KeySize-labelMargin
		sb := src.Bounds()
		w, h := boxW, sb.Dy()*boxW/max(sb.Dx(), 1)
		if h > boxH {
			w, h = sb.Dx()*boxH/max(sb.Dy(), 1), boxH
		}
		at := image.Rect((boxW-w)/2, (boxH-h)/2, (boxW-w)/2+w, (boxH-h)/2+h)
		draw.CatmullRom.Scale(key, at, src, sb, draw.Over, nil)
	}

	if label != "" {
		f, err := labelFace()
		if err != nil {
			return nil, fmt.Errorf("loading the label's font: %w", err)
		}
		d := font.Drawer{Dst: key, Src: image.NewUniform(color.White), Face: f}
		width := d.MeasureString(label)
		d.Dot = fixed.Point26_6{X: (fixed.I(KeySize) - width) / 2, Y: fixed.I(KeySize - labelBaseline)}
		d.DrawString(label)
	}
	return key, nil
}

// Render is a key's image in the Mini's own form: a 24-bit BMP, the
// picture transposed (rotated a quarter turn and flipped, as the Mini's
// keys are mounted).
func Render(picture []byte, label string) ([]byte, error) {
	key, err := Draw(picture, label)
	if err != nil {
		return nil, err
	}
	return nativeBMP(key), nil
}

// nativeBMP is img, KeySize square, transposed, as a BMP file: a 54-byte
// header, then the rows from the bottom up, each pixel blue, green, red.
func nativeBMP(img *image.RGBA) []byte {
	const header = 54
	rowLen := (KeySize*3 + 3) &^ 3
	size := header + rowLen*KeySize
	out := make([]byte, size)
	copy(out, "BM")
	binary.LittleEndian.PutUint32(out[2:], uint32(size))
	binary.LittleEndian.PutUint32(out[10:], header)
	binary.LittleEndian.PutUint32(out[14:], 40) // the info header's size
	binary.LittleEndian.PutUint32(out[18:], KeySize)
	binary.LittleEndian.PutUint32(out[22:], KeySize)
	binary.LittleEndian.PutUint16(out[26:], 1)  // planes
	binary.LittleEndian.PutUint16(out[28:], 24) // bits per pixel
	binary.LittleEndian.PutUint32(out[34:], uint32(rowLen*KeySize))
	binary.LittleEndian.PutUint32(out[38:], 2835) // 72 DPI, as pixels per meter
	binary.LittleEndian.PutUint32(out[42:], 2835)
	for y := range KeySize {
		row := out[header+(KeySize-1-y)*rowLen:]
		for x := range KeySize {
			c := img.RGBAAt(y, x) // transposed
			row[x*3], row[x*3+1], row[x*3+2] = c.B, c.G, c.R
		}
	}
	return out
}
