package main

import (
	_ "embed"
	"image"

	"fyne.io/fyne/v2"
	pigo "github.com/esimov/pigo/core"
)

// facefinder is pigo's frontal face cascade.
//
//go:embed facefinder
var facefinder []byte

// faceStep is how many pixels across one pixel of the picture faces are
// looked for in is, a quarter of the work and still finding faces a few
// meters away at 720p.
const faceStep = 2

// motionStep is the side of the blocks motion is looked for in, big enough
// to average out the camera's noise.
const motionStep = 16

// gray returns img in grayscale, cols by rows pixels each the average luma of
// a step by step block of it.
func gray(img *image.RGBA, step int) (pix []uint8, cols, rows int) {
	b := img.Bounds()
	cols, rows = b.Dx()/step, b.Dy()/step
	pix = make([]uint8, cols*rows)
	for r := range rows {
		for c := range cols {
			sum := 0
			for y := range step {
				i := img.PixOffset(b.Min.X+c*step, b.Min.Y+r*step+y)
				for x := range step {
					p := img.Pix[i+x*4 : i+x*4+3 : i+x*4+3]
					sum += 299*int(p[0]) + 587*int(p[1]) + 114*int(p[2])
				}
			}
			pix[r*cols+c] = uint8(sum / (1000 * step * step))
		}
	}
	return pix, cols, rows
}

// findFaces returns where the faces in img are.
func findFaces(cascade *pigo.Pigo, img *image.RGBA) []image.Rectangle {
	pix, cols, rows := gray(img, faceStep)
	dets := cascade.RunCascade(pigo.CascadeParams{
		MinSize:     40,
		MaxSize:     min(cols, rows),
		ShiftFactor: 0.1,
		ScaleFactor: 1.1,
		ImageParams: pigo.ImageParams{Pixels: pix, Rows: rows, Cols: cols, Dim: cols},
	}, 0)
	var faces []image.Rectangle
	for _, d := range cascade.ClusterDetections(dets, 0.2) {
		if d.Q < 5 { // ponytail: fixed quality cut, a slider if it misses faces in bad light
			continue
		}
		h := d.Scale / 2
		faces = append(faces, scale(image.Rect(d.Col-h, d.Row-h, d.Col+h, d.Row+h), faceStep))
	}
	return faces
}

// moved returns the box around the blocks that changed from prev to cur,
// grayscale pictures cols blocks wide made by gray with motionStep, or an
// empty one when too little changed to be more than noise.
func moved(prev, cur []uint8, cols int) image.Rectangle {
	var box image.Rectangle
	n := 0
	for i := range cur {
		if d := int(cur[i]) - int(prev[i]); d > 25 || d < -25 {
			box = box.Union(image.Rect(i%cols, i/cols, i%cols+1, i/cols+1))
			n++
		}
	}
	if n < 3 { // ponytail: one box around everything that moved, blobs if two things moving should get a box each
		return image.Rectangle{}
	}
	return scale(box, motionStep)
}

func scale(r image.Rectangle, k int) image.Rectangle {
	return image.Rectangle{r.Min.Mul(k), r.Max.Mul(k)}
}

// onScreen returns where r, in a picture pic big, shows in a Camera of size
// area, which fits the picture inside it, centered.
func onScreen(r image.Rectangle, pic image.Point, area fyne.Size) (fyne.Position, fyne.Size) {
	s := min(area.Width/float32(pic.X), area.Height/float32(pic.Y))
	x := (area.Width-float32(pic.X)*s)/2 + float32(r.Min.X)*s
	y := (area.Height-float32(pic.Y)*s)/2 + float32(r.Min.Y)*s
	return fyne.NewPos(x, y), fyne.NewSize(float32(r.Dx())*s, float32(r.Dy())*s)
}
