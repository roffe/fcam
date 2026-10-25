package main

import (
	"image"
	"image/color"
	"image/draw"
	"testing"

	"fyne.io/fyne/v2"
	pigo "github.com/esimov/pigo/core"
)

func TestFindFacesBlank(t *testing.T) {
	cascade, err := pigo.NewPigo().Unpack(facefinder)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.Gray{90}), image.Point{}, draw.Src)
	if faces := findFaces(cascade, img); len(faces) != 0 {
		t.Errorf("found %v in a blank picture", faces)
	}
}

func TestMoved(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.Gray{90}), image.Point{}, draw.Src)
	prev, cols, _ := gray(img, motionStep)
	if cur, _, _ := gray(img, motionStep); !moved(prev, cur, cols).Empty() {
		t.Error("motion between the same pictures")
	}

	draw.Draw(img, image.Rect(64, 32, 128, 96), image.NewUniform(color.White), image.Point{}, draw.Src)
	cur, _, _ := gray(img, motionStep)
	if got := moved(prev, cur, cols); got != image.Rect(64, 32, 128, 96) {
		t.Errorf("moved = %v", got)
	}
}

func TestOnScreen(t *testing.T) {
	// A 4:3 picture in a wide widget gets bars left and right.
	pos, size := onScreen(image.Rect(0, 0, 320, 240), image.Pt(640, 480), fyne.NewSize(800, 300))
	if pos != fyne.NewPos(200, 0) || size != fyne.NewSize(200, 150) {
		t.Errorf("onScreen = %v %v", pos, size)
	}
}
