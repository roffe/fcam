// Command viewer shows a camera picked from a list of the system's video inputs.
package main

import (
	"image"
	"image/jpeg"
	"image/png"
	"strconv"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/storage"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/roffe/fcam"
)

func main() {
	a := app.NewWithID("com.roffe.fcam.viewer")
	w := a.NewWindow("fcam")
	cam := fcam.NewCamera()
	status := widget.NewLabel("")

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
	res := widget.NewSelect(nil, nil)
	res.PlaceHolder = "Resolution"
	rate := widget.NewSelect(nil, nil)
	rate.PlaceHolder = "FPS"
	rate.Disable()
	var sizes []fcam.Resolution
	var rates []float64
	open := func() {
		i := pick.SelectedIndex()
		if i < 0 {
			return
		}
		var want fcam.Resolution // the default, nearest 1280x720
		if j := res.SelectedIndex(); j >= 0 {
			want = sizes[j]
		}
		var fps float64 // the most at that resolution
		if j := rate.SelectedIndex(); j >= 0 {
			fps = rates[j]
		}
		status.SetText("Opening " + devs[i].Name + "...")
		cam.Open(devs[i], want, fps, func(err error) {
			if err != nil {
				status.SetText(err.Error())
				return
			}
			status.SetText("")
		})
	}
	pick.OnChanged = func(string) {
		sizes, _ = devs[pick.SelectedIndex()].Resolutions()
		names := make([]string, len(sizes))
		for j, r := range sizes {
			names[j] = r.String()
		}
		setOptions(res, names)
		rates = nil
		setOptions(rate, nil)
		open()
	}
	res.OnChanged = func(string) {
		rates, _ = devs[pick.SelectedIndex()].FrameRates(sizes[res.SelectedIndex()])
		names := make([]string, len(rates))
		for j, r := range rates {
			names[j] = strconv.FormatFloat(r, 'f', -1, 64) + " fps"
		}
		setOptions(rate, names)
		open()
	}
	rate.OnChanged = func(string) { open() }

	capture := widget.NewButtonWithIcon("Capture", theme.MediaPhotoIcon(), func() {
		if img := cam.Capture(); img != nil {
			save(img, w, status)
		}
	})
	capture.Disable()
	cam.OnChanged = func() {
		if cam.Running() {
			capture.Enable()
			return
		}
		capture.Disable()
		if status.Text == "" {
			status.SetText("The camera stopped")
		}
	}
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

	w.SetContent(container.NewBorder(container.NewBorder(nil, nil, nil, container.NewHBox(res, rate, capture), pick), status, nil, nil, cam))
	w.Resize(fyne.NewSize(960, 640))
	w.ShowAndRun()
}

// setOptions gives s the options names, with none of them selected.
func setOptions(s *widget.Select, names []string) {
	s.Options = names
	s.Selected = ""
	s.Refresh()
	if len(names) == 0 {
		s.Disable()
	} else {
		s.Enable()
	}
}

// save asks where to save img, as a PNG when the name ends in .png and as a
// JPEG otherwise.
func save(img image.Image, w fyne.Window, status *widget.Label) {
	d := dialog.NewFileSave(func(f fyne.URIWriteCloser, err error) {
		if f == nil || err != nil {
			if err != nil {
				status.SetText(err.Error())
			}
			return
		}
		if f.URI().Extension() == ".png" {
			err = png.Encode(f, img)
		} else {
			err = jpeg.Encode(f, img, &jpeg.Options{Quality: 90})
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			status.SetText(err.Error())
			return
		}
		status.SetText("Saved " + f.URI().Name())
	}, w)
	d.SetFileName(time.Now().Format("fcam-20060102-150405.jpg"))
	d.SetFilter(storage.NewExtensionFileFilter([]string{".jpg", ".jpeg", ".png"}))
	d.Show()
}
