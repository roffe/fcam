// Package fcam is a Fyne widget that shows a camera or another video input,
// through the system's own capture API: V4L2 on Linux and the BSDs, Media
// Foundation on Windows, AVFoundation on macOS and iOS, and Camera2 on
// Android. Pictures are drawn as a canvas.VideoFrame, so painters convert them
// to RGB on the GPU.
//
// Mobile apps have to declare that they use the camera: the Android manifest
// needs the android.permission.CAMERA permission (see examples/viewer/AndroidManifest.xml),
// and the Info.plist of an iOS or a bundled macOS app needs
// NSCameraUsageDescription. Open asks the user for access when it has to.
package fcam

import (
	"cmp"
	"errors"
	"image"
	"image/color"
	"math"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// Device is a camera or another video input.
type Device struct {
	// ID is what the system knows the device by: a V4L2 device path, a Media
	// Foundation symbolic link, an AVCaptureDevice uniqueID or a Camera2 id.
	ID string
	// Name is what to show the user.
	Name string
}

// Devices lists the video inputs the system has.
func Devices() ([]Device, error) {
	return devices()
}

// Resolution is the size of the pictures a device sends, in pixels.
type Resolution struct {
	Width, Height int
}

func (r Resolution) String() string {
	return strconv.Itoa(r.Width) + "x" + strconv.Itoa(r.Height)
}

// Resolutions lists the sizes of the pictures d can send, largest first.
func (d Device) Resolutions() ([]Resolution, error) {
	all, err := modes(d.ID)
	list := make([]Resolution, len(all))
	for i, m := range all {
		list[i] = m.Resolution
	}
	slices.SortFunc(list, func(a, b Resolution) int {
		return cmp.Or(b.Width*b.Height-a.Width*a.Height, b.Width-a.Width)
	})
	return slices.Compact(list), err
}

// FrameRates lists the frame rates, in pictures a second, d can send
// pictures of size res at, highest first.
func (d Device) FrameRates(res Resolution) ([]float64, error) {
	all, err := modes(d.ID)
	var list []float64
	for _, m := range all {
		if m.Resolution == res && m.fps > 0 {
			list = append(list, math.Round(m.fps*100)/100)
		}
	}
	slices.Sort(list)
	slices.Reverse(list)
	return slices.Compact(list), err
}

// mode is a size and frame rate a device sends pictures at, 0 when it
// doesn't say.
type mode struct {
	Resolution
	fps float64
}

// modeList pairs up the sizes and frame rates platform code lists.
func modeList[T ~int32 | ~uint32, F ~float64](w, h []T, fps []F) []mode {
	list := make([]mode, len(w))
	for i := range list {
		list[i] = mode{Resolution{int(w[i]), int(h[i])}, float64(fps[i])}
	}
	return list
}

// Camera is a widget that shows the pictures of a Device as they come. It is
// only the picture: it has no controls and takes no input, so apps put their
// own around it or over it. It can be extended like other widgets, by calling
// ExtendBaseWidget on the type that embeds it.
type Camera struct {
	widget.BaseWidget

	// OnChanged is called on the main goroutine when a device starts or
	// stops running, after Open and Close or when the device goes away.
	OnChanged func()

	frame canvas.VideoFrame
	turn  atomic.Int32 // see displayRotation

	opens atomic.Uint64 // calls of Open and Close, the latest of which wins
	opMu  sync.Mutex
	stop  func() // the running device's, under opMu

	mu      sync.Mutex
	next    *picture // the newest picture, not shown yet
	shown   *picture
	spare   *picture
	queued  bool // a publish is on its way to the main goroutine
	running bool
}

// NewCamera returns a Camera showing nothing.
func NewCamera() *Camera {
	c := &Camera{}
	c.ExtendBaseWidget(c)
	return c
}

// CreateRenderer is a private method to Fyne which links this widget to its renderer.
func (c *Camera) CreateRenderer() fyne.WidgetRenderer {
	c.frame.FillMode = canvas.ImageFillContain
	return widget.NewSimpleRenderer(container.NewStack(canvas.NewRectangle(color.Black), &c.frame))
}

// Resize sets a new size for the widget.
func (c *Camera) Resize(s fyne.Size) {
	if s == c.Size() {
		return
	}
	c.BaseWidget.Resize(s)
	c.turn.Store(int32(displayRotation())) // ponytail: a half turn keeps the size and goes unseen
}

// Open starts showing d in place of what the camera showed, without waiting
// for the device, at the one of its resolutions nearest res, or nearest
// 1280x720 when res is zero, and at fps pictures a second, or as many as it
// sends at that resolution when fps is 0. It calls done, if not nil, on the
// main goroutine once the device runs or fails to, unless a later Open or
// Close came first.
func (c *Camera) Open(d Device, res Resolution, fps float64, done func(error)) {
	if res == (Resolution{}) {
		res = Resolution{1280, 720}
	}
	gen := c.opens.Add(1)
	go func() {
		err := c.switchTo(gen, d.ID, mode{res, fps})
		if done != nil && c.opens.Load() == gen {
			fyne.Do(func() { done(err) })
		}
	}()
}

// Close stops the device the camera shows and clears the picture.
func (c *Camera) Close() {
	gen := c.opens.Add(1)
	go c.switchTo(gen, "", mode{})
}

// Running reports whether the camera shows a running device: from when
// Open's device starts until Close, or until the device goes away, which
// leaves its last picture showing.
func (c *Camera) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

func (c *Camera) setRunning(on bool) {
	c.mu.Lock()
	changed := c.running != on
	c.running = on
	c.mu.Unlock()
	if changed {
		fyne.Do(func() {
			if c.OnChanged != nil {
				c.OnChanged()
			}
		})
	}
}

// switchTo stops the running device and starts id, if not empty, for the
// gen'th call of Open or Close, unless a later one came.
func (c *Camera) switchTo(gen uint64, id string, m mode) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if c.opens.Load() != gen {
		return nil
	}
	if c.stop != nil {
		c.stop()
		c.stop = nil
	}
	var err error
	if id != "" {
		c.stop, err = c.start(id, m)
	}
	if c.stop == nil {
		c.setRunning(false)
		fyne.Do(func() {
			if c.opens.Load() == gen {
				c.mu.Lock()
				c.next, c.shown = nil, nil
				c.mu.Unlock()
				c.show(nil)
			}
		})
	}
	return err
}

// start opens id in mode m and hands its pictures to emit until stop is called. One
// goroutine, on a thread of its own, opens, reads and closes the device, as
// some systems want.
func (c *Camera) start(id string, m mode) (stop func(), err error) {
	ready := make(chan error)
	done := make(chan struct{})
	var quit atomic.Bool
	go func() {
		defer close(done)
		runtime.LockOSThread()
		s, err := open(id, m)
		if err == nil {
			c.setRunning(true)
		}
		ready <- err
		if err != nil {
			return
		}
		defer s.close()
		var p picture
		for !quit.Load() {
			ok, err := s.next(&p)
			if err != nil {
				fyne.LogError("fcam: "+id, err)
				c.setRunning(false)
				return
			}
			if ok {
				c.emit(&p)
			}
		}
	}()
	if err := <-ready; err != nil {
		return nil, err
	}
	return func() {
		quit.Store(true)
		<-done
	}, nil
}

// emit copies p, which the device reuses, to show it next.
func (c *Camera) emit(p *picture) {
	c.mu.Lock()
	b := c.spare
	c.spare = nil
	c.mu.Unlock()
	if b == nil {
		b = new(picture)
	}
	b.copyFrom(p)

	c.mu.Lock()
	if c.spare == nil {
		c.spare = c.next
	}
	c.next = b
	queue := !c.queued
	c.queued = true
	c.mu.Unlock()
	if queue {
		fyne.Do(c.publish)
	}
}

func (c *Camera) publish() {
	c.mu.Lock()
	b := c.next
	c.next = nil
	c.queued = false
	if b != nil {
		if c.spare == nil {
			c.spare = c.shown
		}
		c.shown = b
	}
	c.mu.Unlock()
	if b != nil {
		c.show(b)
	}
}

// Capture returns the newest picture of the camera in RGB, turned as it is
// shown, or nil when it shows none. Encode it with image/png or image/jpeg to
// save it, or show it with canvas.NewImageFromImage. It is safe to call from
// any goroutine.
func (c *Camera) Capture() *image.RGBA {
	var p picture
	c.mu.Lock()
	newest := c.next
	if newest == nil {
		newest = c.shown
	}
	if newest != nil {
		p.copyFrom(newest)
	}
	c.mu.Unlock()
	if newest == nil {
		return nil
	}
	return p.rgba(upright(p.rotation, p.front, int(c.turn.Load())))
}

// show points the frame at p, or at nothing when p is nil. The painter reads
// the planes only as it draws, on the main goroutine, so the picture shown
// before is free once this returns.
func (c *Camera) show(p *picture) {
	f := &c.frame
	if p == nil {
		f.Width, f.Height = 0, 0
		f.Y, f.U, f.V, f.UV = nil, nil, nil, nil
		f.Refresh()
		return
	}
	f.Width, f.Height = p.width, p.height
	f.Y, f.YStride = p.y, p.yStride
	f.U, f.V, f.UV, f.CStride = p.u, p.v, p.uv, p.cStride
	f.ColorSpace, f.FullRange = p.colorSpace, p.fullRange
	f.Rotation = upright(p.rotation, p.front, int(c.turn.Load()))
	f.Refresh()
}

// picture is a picture in I420, or in NV12 when u is nil, with how the
// device's sensor is turned.
type picture struct {
	width, height    int
	y, u, v, uv      []byte
	yStride, cStride int
	colorSpace       canvas.VideoColorSpace
	fullRange        bool

	// rotation is how many degrees clockwise the picture turns to stand
	// upright with the device held as it naturally is, and front says that
	// the camera faces the user.
	rotation int
	front    bool
}

// rgba converts p to RGB, turned clockwise by turn degrees.
func (p *picture) rgba(turn int) *image.RGBA {
	kr, kb := float32(0.299), float32(0.114)
	switch p.colorSpace {
	case canvas.VideoColorSpaceBT709:
		kr, kb = 0.2126, 0.0722
	case canvas.VideoColorSpaceBT2020:
		kr, kb = 0.2627, 0.0593
	}
	yOff, yScale, cScale := float32(0), float32(1)/255, float32(1)/255
	if !p.fullRange {
		yOff, yScale, cScale = 16, float32(1)/219, float32(1)/224
	}
	w, h := p.width, p.height
	out := image.Rect(0, 0, w, h)
	if turn%180 != 0 {
		out = image.Rect(0, 0, h, w)
	}
	img := image.NewRGBA(out)
	for y := range h {
		row := y / 2 * p.cStride
		for x := range w {
			var u, v byte
			if p.u == nil {
				u, v = p.uv[row+x/2*2], p.uv[row+x/2*2+1]
			} else {
				u, v = p.u[row+x/2], p.v[row+x/2]
			}
			l := (float32(p.y[y*p.yStride+x]) - yOff) * yScale
			cb, cr := (float32(u)-128)*cScale, (float32(v)-128)*cScale
			r, b := l+2*(1-kr)*cr, l+2*(1-kb)*cb
			g := (l - kr*r - kb*b) / (1 - kr - kb)
			dx, dy := x, y
			switch turn {
			case 90:
				dx, dy = h-1-y, x
			case 180:
				dx, dy = w-1-x, h-1-y
			case 270:
				dx, dy = y, w-1-x
			}
			i := img.PixOffset(dx, dy)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = clamp(r), clamp(g), clamp(b), 0xff
		}
	}
	return img
}

func clamp(v float32) uint8 {
	return uint8(min(max(v*255+0.5, 0), 255))
}

func (p *picture) copyFrom(src *picture) {
	y, u, v, uv := p.y, p.u, p.v, p.uv
	*p = *src
	p.y, p.u, p.v, p.uv = clone(y, src.y), clone(u, src.u), clone(v, src.v), clone(uv, src.uv)
}

func clone(dst, src []byte) []byte {
	if src == nil {
		return nil
	}
	return append(dst[:0], src...)
}

func grow(b []byte, n int) []byte {
	if cap(b) < n {
		return make([]byte, n)
	}
	return b[:n]
}

// upright is how far a picture turns clockwise to stand upright on a screen
// turned display degrees counterclockwise, for a sensor turned rotation.
func upright(rotation int, front bool, display int) int {
	if front {
		return (rotation + display) % 360
	}
	return (rotation - display + 360) % 360
}

// displayRotation is how many degrees counterclockwise the screen is turned
// from how the device is naturally held. Android asks the system; elsewhere a
// mobile device is taken to be naturally upright and turned left when wide.
var displayRotation = func() int {
	d := fyne.CurrentDevice()
	if d.IsMobile() && fyne.IsHorizontal(d.Orientation()) {
		return 90
	}
	return 0
}

// parseDevices reads the lines of "id\tname" the platform code lists devices in.
func parseDevices(s string) []Device {
	var list []Device
	for line := range strings.Lines(s) {
		id, name, _ := strings.Cut(strings.TrimSuffix(line, "\n"), "\t")
		if id != "" {
			list = append(list, Device{ID: id, Name: name})
		}
	}
	return list
}

var (
	errUnsupported = errors.New("fcam: no camera support on " + runtime.GOOS)
	errNoFormat    = errors.New("fcam: the device sends no picture format fcam can show")
)
