# fcam

A camera widget for [Fyne](https://fyne.io). Webcams, capture cards and phone cameras, through each system's own capture API, so there's no FFmpeg or anything else to install or bundle.

The pictures go to the painter as YUV in a `canvas.VideoFrame` and get turned into RGB on the GPU. There's a QR code reader built on it too, see [Reading QR codes](#reading-qr-codes).

## Platforms

| | Capture API | Status |
|---|---|---|
| Linux, BSD | V4L2 | Tested with a Logitech C920 |
| Windows | Media Foundation | Tested on a Surface Pro 7, front and rear camera |
| macOS, iOS | AVFoundation | Not built yet, needs a Mac |
| Android | Camera2 | Tested on a Lenovo Tab M10, front and rear camera |

fcam opens a camera at the resolution you pick from the ones it lists, or the closest to 1280x720 by default, and at the frame rate you pick, or the highest it has there. On V4L2 it takes NV12, I420, MJPEG or YUYV, the first one the camera has at that size in that order, MJPEG decoded with Go's `image/jpeg`. Media Foundation converts whatever the camera sends to NV12 by itself, and AVFoundation and Camera2 hand over YUV as it is.

## Using the widget

`fcam.Camera` is just the picture. No controls and no mouse handling, so you put your own around it or on top of it.

```go
package main

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
	"github.com/roffe/fcam"
)

func main() {
	a := app.NewWithID("com.example.camera")
	w := a.NewWindow("Camera")
	cam := fcam.NewCamera()
	stop := widget.NewButton("Stop", cam.Close)
	stop.Disable()
	cam.OnChanged = func() { // on the main goroutine when the camera starts or stops
		if cam.Running() {
			stop.Enable()
		} else {
			stop.Disable()
		}
	}
	w.SetContent(container.NewStack(cam, container.NewVBox(stop))) // the button over the picture
	w.Resize(fyne.NewSize(960, 600))

	a.Lifecycle().SetOnStarted(func() {
		devs, err := fcam.Devices()
		if err != nil || len(devs) == 0 {
			fyne.LogError("no camera", err)
			return
		}
		cam.Open(devs[0], fcam.Resolution{}, 0, func(err error) { // the default resolution and frame rate
			if err != nil {
				fyne.LogError("can't open "+devs[0].Name, err)
			}
		})
	})
	w.ShowAndRun()
}
```

`Devices` lists the cameras, each with a `Name` to show and an `ID` the system knows it by. `Open` starts one in place of what the camera showed and returns at once, opening a camera can take a while, and on mobile the user has to answer the permission question first. It calls back on the main goroutine when the camera runs or didn't make it. The last `Open` or `Close` wins, so switching cameras is just another `Open`.

`Resolutions` lists the sizes a camera sends, largest first, and `FrameRates` the frame rates it sends at one of them, fastest first. `Open` takes the ones to use. A zero `Resolution` gets the one closest to 1280x720, and a 0 frame rate the highest at that size:

```go
sizes, err := devs[0].Resolutions() // [1920x1080 1280x720 640x480 ...]
if err != nil || len(sizes) == 0 {
	return
}
rates, err := devs[0].FrameRates(sizes[0]) // [30 24 20 15 10 7.5 5]
if err != nil || len(rates) == 0 {
	return
}
cam.Open(devs[0], sizes[0], rates[len(rates)-1], nil) // the largest, as slow as it goes
```

Webcams stretch the exposure when it's dark, so they can send fewer pictures than you asked for.

`Close` stops the camera and clears the picture. `Running` says if a camera runs, and `OnChanged` gets called on the main goroutine when that changes, also when a camera goes away by itself (unplugged, or another app took it on a phone). Then its last picture stays up until you `Close` or `Open` something else. `Open`, `Close` and `Running` are safe to call from any goroutine.

`Capture` gives you the newest picture as an `*image.RGBA`, turned the way it shows, or nil when there's nothing to show. Save it as whatever Go can encode, or put it on screen with `canvas.NewImageFromImage`:

```go
img := cam.Capture()
if img == nil {
	return
}
f, err := os.Create("shot.jpg")
if err != nil {
	return
}
defer f.Close()
jpeg.Encode(f, img, &jpeg.Options{Quality: 90}) // or png.Encode(f, img)
```

It's safe to call from any goroutine, a 1280x720 picture takes about 8 ms.

Extend it like any Fyne widget:

```go
type myCamera struct {
	fcam.Camera
}

func newMyCamera() *myCamera {
	c := &myCamera{}
	c.ExtendBaseWidget(c)
	return c
}
```

## Reading QR codes

The `qr` package has a `Scanner`, a `Camera` that reads the QR codes it sees with [gozxing](https://github.com/makiuchi-d/gozxing), a Go port of ZXing, so still no C libraries. Only apps that import `qr` get gozxing compiled in.

```go
scanner := qr.NewScanner()
scanner.OnCode = func(text string) { // on the main goroutine
	fmt.Println("read", text)
}
scanner.Open(dev, fcam.Resolution{}, 0, nil)
```

It looks for a code five times a second while the camera runs, about 25 ms of CPU each time at 1280x720. A code gets reported when it comes into view, holding it there or taking it away for less than two seconds doesn't report it again. `Close` stops the reading too. Small or far away codes read better at a higher resolution.

## Examples

[examples/viewer](examples/viewer/main.go) has dropdowns for the camera, its resolution and frame rate, and a Capture button that saves the picture as a JPEG, or a PNG when the name ends in `.png`. [examples/qr](examples/qr/main.go) reads QR codes, with buttons to copy what it read and to open it when it's a link.

```sh
go run ./examples/viewer
go run ./examples/qr
```

## Permissions

Android apps need the camera permission in their manifest, fyne package uses an `AndroidManifest.xml` it finds next to the code, see [examples/viewer/AndroidManifest.xml](examples/viewer/AndroidManifest.xml). `Open` asks the user for it when the app doesn't have it yet.

iOS apps, and bundled macOS apps, need `NSCameraUsageDescription` in their Info.plist or the system kills them when they touch the camera. The fyne tool can't add it yet, so it has to go in by hand. Run unbundled on macOS it's the terminal that gets asked.

Mobile systems take the camera away from apps in the background, the examples close it when it leaves the foreground and opens it again when it comes back.

## Building

You need Go 1.27 with cgo. Fyne comes from my fork's `wgpu` branch, go.mod points there. It has the `canvas.VideoFrame` the widget draws with, upstream Fyne doesn't.

The Makefile builds both examples into `dist`, as `fcam_viewer_*` and `fcam_qr_*`:

- `make linux` builds `dist/fcam_viewer_linux_x64` and `dist/fcam_qr_linux_x64`, they need nothing on top of what any Fyne app takes
- `make android` builds the APKs with the NDK r30 in `~/Android`, `NDK=` picks another
- `make install-android` installs them on the device adb sees
- `make windows` cross compiles the exes with mingw, single files that need no DLLs. `WINCC=` picks another compiler

The NDK camera library came with Android 7 but fyne builds for Android 5, so fcam loads it when it's first used.

## Known limits

- Front cameras aren't mirrored like a selfie preview usually is
- On Android the rotation is checked when the widget resizes, turning the phone upside down doesn't resize it so that goes unnoticed
- MJPEG gets decoded on the CPU, around 30% of one core for 720p at 30 fps

## Tests

```sh
go test ./...
```

The capture test reads from the first camera it finds and gets skipped when there is none. The `qr` tests read a generated code, no camera needed.
