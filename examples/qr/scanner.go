package main

import (
	"image"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
	"github.com/roffe/fcam"
)

// Scanner is a camera that reads the QR codes it sees, a few times a second
// while it runs. It is opened and closed as an fcam.Camera is, and can be
// extended the same way.
type Scanner struct {
	fcam.Camera

	// OnCode is called on the main goroutine with the text of a QR code as it
	// comes into view. A code that stays in view, or is gone for less than
	// two seconds, is reported once.
	OnCode func(text string)

	mu   sync.Mutex
	stop chan struct{} // the scanning goroutine's, nil when none runs
}

// NewScanner returns a Scanner showing nothing.
func NewScanner() *Scanner {
	s := &Scanner{}
	s.ExtendBaseWidget(s)
	return s
}

// Open starts showing d and reading the codes it sees, see fcam.Camera.Open.
func (s *Scanner) Open(d fcam.Device, res fcam.Resolution, fps float64, done func(error)) {
	s.Camera.Open(d, res, fps, done)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop == nil {
		s.stop = make(chan struct{})
		go s.scan(s.stop)
	}
}

// Close stops the device and reading codes, see fcam.Camera.Close.
func (s *Scanner) Close() {
	s.Camera.Close()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
}

func (s *Scanner) scan(stop <-chan struct{}) {
	reader := qrcode.NewQRCodeReader()
	tick := time.NewTicker(time.Second / 5)
	defer tick.Stop()
	var seen sightings
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		if !s.Running() {
			continue
		}
		img := s.Capture()
		if img == nil {
			continue
		}
		text, ok := read(reader, img)
		if ok && seen.add(text, time.Now()) {
			fyne.Do(func() {
				if s.OnCode != nil {
					s.OnCode(text)
				}
			})
		}
	}
}

// read returns the text of the QR code in img, if it finds one.
func read(reader gozxing.Reader, img image.Image) (string, bool) {
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		return "", false
	}
	res, err := reader.Decode(bmp, nil)
	if err != nil {
		return "", false
	}
	return res.GetText(), true
}

// sightings tells codes coming into view from ones still in it.
type sightings struct {
	last string
	at   time.Time
}

// add notes text seen at now, and reports whether it just came into view.
func (s *sightings) add(text string, now time.Time) bool {
	isNew := text != s.last || now.Sub(s.at) > 2*time.Second
	s.last, s.at = text, now
	return isNew
}
