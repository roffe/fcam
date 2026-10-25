// Command qr reads QR codes with a camera picked from a list of the system's
// video inputs.
package main

import (
	"net/url"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/roffe/fcam"
)

func main() {
	a := app.NewWithID("com.roffe.fcam.qr")
	w := a.NewWindow("fcam QR")
	scanner := NewScanner()
	info := widget.NewLabel("Hold a QR code up to the camera")
	info.Wrapping = fyne.TextWrapWord

	devs, err := fcam.Devices()
	if err != nil {
		info.SetText(err.Error())
	} else if len(devs) == 0 {
		info.SetText("No cameras found")
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
		scanner.Open(devs[i], fcam.Resolution{}, 0, func(err error) {
			if err != nil {
				info.SetText(err.Error())
			}
		})
	}
	pick.OnChanged = func(string) { open() }

	var link *url.URL
	copyCode := widget.NewButtonWithIcon("Copy", theme.ContentCopyIcon(), func() {
		a.Clipboard().SetContent(info.Text)
	})
	openLink := widget.NewButton("Open link", func() {
		if err := a.OpenURL(link); err != nil {
			info.SetText(err.Error())
		}
	})
	copyCode.Disable()
	openLink.Disable()
	scanner.OnCode = func(text string) {
		info.SetText(text)
		copyCode.Enable()
		link = nil
		openLink.Disable()
		if u, err := url.Parse(text); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			link = u
			openLink.Enable()
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
		a.Lifecycle().SetOnExitedForeground(scanner.Close)
	}

	result := container.NewBorder(nil, nil, nil, container.NewHBox(copyCode, openLink), info)
	w.SetContent(container.NewBorder(pick, result, nil, nil, scanner))
	w.Resize(fyne.NewSize(800, 640))
	w.ShowAndRun()
}
