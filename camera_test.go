package fcam

import (
	"image"
	"image/color"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
)

type extended struct{ Camera }

// TestExtend shows a picture in a widget embedding Camera, set up only by
// ExtendBaseWidget, and follows it starting and stopping.
func TestExtend(t *testing.T) {
	test.NewTempApp(t)
	c := &extended{}
	c.ExtendBaseWidget(c)
	changes := 0
	c.OnChanged = func() { changes++ }
	w := test.NewTempWindow(t, c)
	w.Resize(fyne.NewSize(64, 48))

	c.setRunning(true)
	c.emit(&picture{width: 2, height: 2, y: []byte{235, 235, 235, 235}, uv: []byte{128, 128}, yStride: 2, cStride: 2})
	if img := w.Canvas().Capture(); c.frame.Width != 2 || img.Bounds().Empty() {
		t.Fatalf("frame %dx%d after a picture", c.frame.Width, c.frame.Height)
	}
	if img := c.Capture(); img == nil || img.Bounds() != image.Rect(0, 0, 2, 2) {
		t.Fatalf("Capture() = %v", img)
	}
	c.setRunning(false)
	if c.Running() || changes != 2 {
		t.Errorf("Running %v after %d changes, want false after 2", c.Running(), changes)
	}
}

func TestUpright(t *testing.T) {
	for _, c := range []struct {
		rotation, display int
		front             bool
		want              int
	}{
		{0, 0, false, 0},
		{90, 0, false, 90},
		{90, 90, false, 0},
		{90, 270, false, 180},
		{270, 0, true, 270},
		{270, 90, true, 0},
	} {
		if got := upright(c.rotation, c.front, c.display); got != c.want {
			t.Errorf("upright(%d, %v, %d) = %d, want %d", c.rotation, c.front, c.display, got, c.want)
		}
	}
}

func TestRGBA(t *testing.T) {
	// Video range BT.601 I420, black on the left and white on the right.
	p := &picture{width: 4, height: 2, yStride: 4, cStride: 2,
		y: []byte{16, 16, 235, 235, 16, 16, 235, 235}, u: []byte{128, 128}, v: []byte{128, 128}}
	img := p.rgba(90)
	if img.Bounds() != image.Rect(0, 0, 2, 4) {
		t.Fatalf("turned 90: bounds %v", img.Bounds())
	}
	if top, bottom := img.RGBAAt(0, 0), img.RGBAAt(1, 3); top != (color.RGBA{0, 0, 0, 255}) || bottom != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("turned 90: top %v, bottom %v, want black over white", top, bottom)
	}

	// Full range BT.601 NV12 red.
	p = &picture{width: 2, height: 2, yStride: 2, cStride: 2, fullRange: true,
		y: []byte{76, 76, 76, 76}, uv: []byte{85, 255}}
	if got := p.rgba(0).RGBAAt(1, 1); got.R < 253 || got.G > 2 || got.B > 2 {
		t.Errorf("red came out %v", got)
	}
}

func TestParseDevices(t *testing.T) {
	list := parseDevices("/dev/video0\tC920\n0\tBack camera (0)\n\n")
	if len(list) != 2 || list[0] != (Device{"/dev/video0", "C920"}) || list[1] != (Device{"0", "Back camera (0)"}) {
		t.Errorf("parseDevices = %v", list)
	}
}

// TestDevice reads pictures from the first device the system has, at its
// largest resolution, and at its smallest at the lowest frame rate there.
func TestDevice(t *testing.T) {
	list, err := Devices()
	if err != nil || len(list) == 0 {
		t.Skip("no camera:", err)
	}
	d := list[0]
	sizes, err := d.Resolutions()
	if err != nil || len(sizes) == 0 {
		t.Fatalf("%s: resolutions %v, %v", d.Name, sizes, err)
	}
	small := sizes[len(sizes)-1]
	rates, err := d.FrameRates(small)
	if err != nil || len(rates) == 0 {
		t.Fatalf("%s: frame rates at %v: %v, %v", d.Name, small, rates, err)
	}
	t.Logf("%s: %v, %v at %v", d.Name, sizes, rates, small)

	if got, _ := read(t, d, mode{sizes[0], 0}); got != sizes[0] {
		t.Errorf("%s: opened at %v, got %v", d.Name, sizes[0], got)
	}
	slow := rates[len(rates)-1]
	got, fps := read(t, d, mode{small, slow})
	if got != small || fps > slow*1.3+1 {
		t.Errorf("%s: opened at %v %v fps, got %v %.1f fps", d.Name, small, slow, got, fps)
	}
	_, fast := read(t, d, mode{small, rates[0]})
	t.Logf("%v at %v fps: %.1f fps, at %v fps: %.1f fps (less in the dark)", small, slow, fps, rates[0], fast)
}

// read opens d in mode m and returns the size of its pictures and how many
// came in a second.
func read(t *testing.T, d Device, m mode) (Resolution, float64) {
	s, err := open(d.ID, m)
	if err != nil {
		t.Fatal(d, m, err)
	}
	defer s.close()
	var size Resolution
	var start time.Time
	n := 0
	for tries := 0; tries < 50 || n > 0; tries++ {
		var p picture
		ok, err := s.next(&p)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			continue
		}
		f := &canvas.VideoFrame{Width: p.width, Height: p.height, Y: p.y, YStride: p.yStride, U: p.u, V: p.v, UV: p.uv, CStride: p.cStride}
		if !videoPlanes(f) {
			t.Fatalf("%s: %dx%d picture with planes too short", d.Name, p.width, p.height)
		}
		if n == 0 {
			size, start = Resolution{p.width, p.height}, time.Now()
		}
		if n++; time.Since(start) > time.Second {
			return size, float64(n-1) / time.Since(start).Seconds()
		}
	}
	t.Fatalf("%s: no picture in %v in 5 seconds", d.Name, m)
	return size, 0
}

// videoPlanes is the painters' check that f holds the samples it says it does.
func videoPlanes(f *canvas.VideoFrame) bool {
	cw, ch := (f.Width+1)/2, (f.Height+1)/2
	if f.Width <= 0 || f.Height <= 0 || f.YStride < f.Width || len(f.Y) < (f.Height-1)*f.YStride+f.Width {
		return false
	}
	if f.U == nil {
		return f.CStride >= 2*cw && len(f.UV) >= (ch-1)*f.CStride+2*cw
	}
	return f.CStride >= cw && len(f.U) >= (ch-1)*f.CStride+cw && len(f.V) >= (ch-1)*f.CStride+cw
}
