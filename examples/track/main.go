// Command track draws boxes around the faces, or what moves, a camera picked
// from a list of the system's video inputs sees.
package main

import (
	"image"
	"image/color"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	pigo "github.com/esimov/pigo/core"
	"github.com/roffe/fcam"
)

func main() {
	a := app.NewWithID("com.roffe.fcam.track")
	w := a.NewWindow("fcam track")
	cam := fcam.NewCamera()
	overlay := container.NewWithoutLayout()
	status := widget.NewLabel("")

	cascade, err := pigo.NewPigo().Unpack(facefinder)
	if err != nil {
		fyne.LogError("unpacking the face cascade", err)
		return
	}

	devs, err := fcam.Devices()
	if err != nil {
		status.SetText(err.Error())
	} else if len(devs) == 0 {
		status.SetText("No cameras found")
	}
	names := make([]string, len(devs))
	for i, d := range devs {
		names[i] = d.Name
	}
	pick := widget.NewSelect(names, nil)
	pick.PlaceHolder = "Select a camera"
	open := func() {
		i := pick.SelectedIndex()
		if i < 0 {
			return
		}
		cam.Open(devs[i], fcam.Resolution{}, 0, func(err error) {
			if err != nil {
				status.SetText(err.Error())
			}
		})
	}
	pick.OnChanged = func(string) { open() }

	var faces atomic.Bool
	mode := widget.NewRadioGroup([]string{"Faces", "Motion"}, func(s string) { faces.Store(s == "Faces") })
	mode.Horizontal = true
	mode.Required = true
	mode.SetSelected("Faces")

	show := func(boxes []image.Rectangle, pic image.Point) {
		if !cam.Running() {
			boxes = nil
		}
		for len(overlay.Objects) < len(boxes) {
			r := canvas.NewRectangle(color.Transparent)
			r.StrokeColor = theme.Color(theme.ColorNamePrimary)
			r.StrokeWidth = 3
			overlay.Add(r)
		}
		for i, o := range overlay.Objects {
			if i >= len(boxes) {
				o.Hide()
				continue
			}
			pos, size := onScreen(boxes[i], pic, overlay.Size())
			o.Move(pos)
			o.Resize(size)
			o.Show()
		}
		overlay.Refresh()
	}

	go func() {
		var prev []uint8
		for range time.Tick(time.Second / 15) {
			if !cam.Running() {
				prev = nil
				continue
			}
			img := cam.Capture()
			if img == nil {
				continue
			}
			var boxes []image.Rectangle
			if faces.Load() {
				boxes, prev = findFaces(cascade, img), nil
			} else {
				cur, cols, _ := gray(img, motionStep)
				if len(prev) == len(cur) {
					if b := moved(prev, cur, cols); !b.Empty() {
						boxes = []image.Rectangle{b}
					}
				}
				prev = cur
			}
			pic := img.Bounds().Size()
			fyne.Do(func() { show(boxes, pic) })
		}
	}()

	a.Lifecycle().SetOnStarted(func() {
		if len(devs) > 0 {
			pick.SetSelectedIndex(0)
		}
	})
	if fyne.CurrentDevice().IsMobile() {
		// Mobile systems take the camera away from apps in the background.
		a.Lifecycle().SetOnEnteredForeground(open)
		a.Lifecycle().SetOnExitedForeground(cam.Close)
	}

	top := container.NewBorder(nil, nil, nil, mode, pick)
	w.SetContent(container.NewBorder(top, status, nil, nil, container.NewStack(cam, overlay)))
	w.Resize(fyne.NewSize(800, 640))
	w.ShowAndRun()
}
