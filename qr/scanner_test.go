package qr

import (
	"image"
	"image/color"
	"image/draw"
	"testing"
	"time"

	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
)

func TestRead(t *testing.T) {
	frame := image.NewRGBA(image.Rect(0, 0, 640, 480))
	draw.Draw(frame, frame.Bounds(), image.NewUniform(color.Gray{90}), image.Point{}, draw.Src)
	reader := qrcode.NewQRCodeReader()
	if text, ok := read(reader, frame); ok {
		t.Fatalf("read %q from a picture without a code", text)
	}

	code, err := qrcode.NewQRCodeWriter().Encode("https://github.com/roffe/fcam", gozxing.BarcodeFormat_QR_CODE, 200, 200, nil)
	if err != nil {
		t.Fatal(err)
	}
	draw.Draw(frame, image.Rect(300, 200, 500, 400), code, image.Point{}, draw.Src)
	if text, ok := read(reader, frame); !ok || text != "https://github.com/roffe/fcam" {
		t.Errorf("read %q, %v", text, ok)
	}
}

func TestSightings(t *testing.T) {
	var s sightings
	start := time.Now()
	for _, c := range []struct {
		text  string
		after time.Duration
		isNew bool
	}{
		{"a", 0, true},
		{"a", time.Second, false},     // still in view
		{"a", 2 * time.Second, false}, // gone for less than two seconds
		{"b", 2200 * time.Millisecond, true},
		{"a", 2400 * time.Millisecond, true},
		{"a", 5 * time.Second, true}, // back after a while
	} {
		if got := s.add(c.text, start.Add(c.after)); got != c.isNew {
			t.Errorf("%q after %v: new %v, want %v", c.text, c.after, got, c.isNew)
		}
	}
}
