//go:build (linux && !android) || freebsd || openbsd || netbsd

package fcam

/*
#cgo freebsd CFLAGS: -I/usr/local/include
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/ioctl.h>
#include <sys/mman.h>
#include <unistd.h>
#if defined(__OpenBSD__) || defined(__NetBSD__)
#include <sys/videoio.h>
#else
#include <linux/videodev2.h>
#endif

enum { nbufs = 4 };

typedef struct {
	int fd, n, held;
	void *maps[nbufs];
	size_t lens[nbufs];
	struct v4l2_pix_format pix;
} cam;

static int xioctl(int fd, unsigned long req, void *arg) {
	int r;
	do
		r = ioctl(fd, req, arg);
	while (r < 0 && errno == EINTR);
	return r < 0 ? -errno : 0;
}

// capture reports whether the device at path streams pictures, and puts its name in name.
static int capture(const char *path, char *name, int size) {
	int fd = open(path, O_RDWR | O_NONBLOCK);
	if (fd < 0)
		return 0;
	struct v4l2_capability cap = {0};
	int ok = xioctl(fd, VIDIOC_QUERYCAP, &cap) == 0;
	close(fd);
	if (!ok)
		return 0;
	uint32_t caps = cap.capabilities;
#ifdef V4L2_CAP_DEVICE_CAPS
	if (caps & V4L2_CAP_DEVICE_CAPS)
		caps = cap.device_caps;
#endif
	snprintf(name, size, "%s", (const char *)cap.card);
	return (caps & V4L2_CAP_VIDEO_CAPTURE) && (caps & V4L2_CAP_STREAMING);
}

static int supports(int fd, uint32_t fourcc) {
	struct v4l2_fmtdesc d = {.type = V4L2_BUF_TYPE_VIDEO_CAPTURE};
	for (; xioctl(fd, VIDIOC_ENUM_FMT, &d) == 0; d.index++)
		if (d.pixelformat == fourcc)
			return 1;
	return 0;
}

// has_size reports whether the device sends fourcc at w by h, which a range
// of sizes is taken to when it goes that far.
static int has_size(int fd, uint32_t fourcc, uint32_t w, uint32_t h) {
	struct v4l2_frmsizeenum s = {.pixel_format = fourcc};
	for (; xioctl(fd, VIDIOC_ENUM_FRAMESIZES, &s) == 0; s.index++) {
		if (s.type != V4L2_FRMSIZE_TYPE_DISCRETE)
			return w <= s.stepwise.max_width && h <= s.stepwise.max_height;
		if (s.discrete.width == w && s.discrete.height == h)
			return 1;
	}
	return 0;
}

static double rate(struct v4l2_fract interval) {
	return interval.numerator ? (double)interval.denominator / interval.numerator : 0;
}

// size_rate reports whether the device sends fourcc at w by h, and at fps
// pictures a second unless fps is 0, and puts the most it sends at that size
// in top. A device that doesn't list its frame rates is taken to send any.
static int size_rate(int fd, uint32_t fourcc, uint32_t w, uint32_t h, double fps, double *top) {
	*top = 0;
	if (!has_size(fd, fourcc, w, h))
		return 0;
	struct v4l2_frmivalenum iv = {.pixel_format = fourcc, .width = w, .height = h};
	int found = fps <= 0, any = 0;
	for (; xioctl(fd, VIDIOC_ENUM_FRAMEINTERVALS, &iv) == 0; iv.index++) {
		any = 1;
		double lo, hi;
		if (iv.type == V4L2_FRMIVAL_TYPE_DISCRETE)
			lo = hi = rate(iv.discrete);
		else {
			lo = rate(iv.stepwise.max);
			hi = rate(iv.stepwise.min);
		}
		if (hi > *top)
			*top = hi;
		if (fps > lo - 0.01 && fps < hi + 0.01)
			found = 1;
		if (iv.type != V4L2_FRMIVAL_TYPE_DISCRETE)
			break;
	}
	return found || !any;
}

// cam_modes puts up to n of the sizes and frame rates the device at path
// sends formats at into w, h and fps, and returns how many. A range of sizes
// or frame rates counts as its largest, and a size without frame rates gets 0.
static int cam_modes(const char *path, const uint32_t *formats, int nformats, uint32_t *w, uint32_t *h, double *fps, int n) {
	int fd = open(path, O_RDWR | O_NONBLOCK);
	if (fd < 0)
		return -errno;
	int k = 0;
	for (int i = 0; i < nformats; i++) {
		struct v4l2_frmsizeenum s = {.pixel_format = formats[i]};
		for (; k < n && xioctl(fd, VIDIOC_ENUM_FRAMESIZES, &s) == 0; s.index++) {
			int discrete = s.type == V4L2_FRMSIZE_TYPE_DISCRETE;
			uint32_t sw = discrete ? s.discrete.width : s.stepwise.max_width;
			uint32_t sh = discrete ? s.discrete.height : s.stepwise.max_height;
			struct v4l2_frmivalenum iv = {.pixel_format = formats[i], .width = sw, .height = sh};
			int first = k;
			for (; k < n && xioctl(fd, VIDIOC_ENUM_FRAMEINTERVALS, &iv) == 0; iv.index++) {
				w[k] = sw;
				h[k] = sh;
				fps[k++] = rate(iv.type == V4L2_FRMIVAL_TYPE_DISCRETE ? iv.discrete : iv.stepwise.min);
				if (iv.type != V4L2_FRMIVAL_TYPE_DISCRETE)
					break;
			}
			if (k == first && k < n) {
				w[k] = sw;
				h[k] = sh;
				fps[k++] = 0;
			}
			if (!discrete)
				break;
		}
	}
	close(fd);
	return k;
}

static void cam_close(cam *c) {
	int type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
	xioctl(c->fd, VIDIOC_STREAMOFF, &type);
	for (int i = 0; i < c->n; i++)
		munmap(c->maps[i], c->lens[i]);
	close(c->fd);
}

// cam_open starts streaming the device at path in the first format of
// formats it sends at width by height and fps pictures a second, or with fps
// 0 the one it sends the most pictures of at that size, or else the first it
// has at all, at the size and frame rate nearest that it takes.
static int cam_open(cam *c, const char *path, const uint32_t *formats, int nformats, int width, int height, double fps) {
	c->held = -1;
	c->n = 0;
	c->fd = open(path, O_RDWR | O_NONBLOCK);
	if (c->fd < 0)
		return -errno;
	int i = -1, first = -1;
	double best = -1, top;
	for (int j = 0; j < nformats; j++) {
		if (!supports(c->fd, formats[j]))
			continue;
		if (first < 0)
			first = j;
		if (size_rate(c->fd, formats[j], width, height, fps, &top) && top > best) {
			i = j;
			best = top;
			if (fps > 0)
				break;
		}
	}
	if (i < 0)
		i = first;
	if (i < 0) {
		close(c->fd);
		return -EPROTONOSUPPORT;
	}
	struct v4l2_format f = {.type = V4L2_BUF_TYPE_VIDEO_CAPTURE};
	f.fmt.pix.width = width;
	f.fmt.pix.height = height;
	f.fmt.pix.pixelformat = formats[i];
	f.fmt.pix.field = V4L2_FIELD_NONE;
	struct v4l2_requestbuffers req = {.count = nbufs, .type = V4L2_BUF_TYPE_VIDEO_CAPTURE, .memory = V4L2_MEMORY_MMAP};
	int r = xioctl(c->fd, VIDIOC_S_FMT, &f);
	if (r == 0) {
		// Drivers take the frame rate nearest the one asked, or the highest for 1000.
		struct v4l2_streamparm p = {.type = V4L2_BUF_TYPE_VIDEO_CAPTURE};
		p.parm.capture.timeperframe = fps > 0 ? (struct v4l2_fract){1000, (uint32_t)(fps * 1000 + 0.5)} : (struct v4l2_fract){1, 1000};
		xioctl(c->fd, VIDIOC_S_PARM, &p);
		r = xioctl(c->fd, VIDIOC_REQBUFS, &req);
	}
	c->pix = f.fmt.pix;
	for (; r == 0 && c->n < (int)req.count && c->n < nbufs; c->n++) {
		struct v4l2_buffer b = {.index = c->n, .type = V4L2_BUF_TYPE_VIDEO_CAPTURE, .memory = V4L2_MEMORY_MMAP};
		if ((r = xioctl(c->fd, VIDIOC_QUERYBUF, &b)) < 0)
			break;
		c->maps[c->n] = mmap(NULL, b.length, PROT_READ | PROT_WRITE, MAP_SHARED, c->fd, b.m.offset);
		if (c->maps[c->n] == MAP_FAILED) {
			r = -errno;
			break;
		}
		c->lens[c->n] = b.length;
		r = xioctl(c->fd, VIDIOC_QBUF, &b);
	}
	int type = V4L2_BUF_TYPE_VIDEO_CAPTURE;
	if (r == 0)
		r = xioctl(c->fd, VIDIOC_STREAMON, &type);
	if (r < 0)
		cam_close(c);
	return r;
}

// cam_next gives the buffer the last call returned back to the device, then
// waits up to 100 ms for the next picture and returns its buffer, or -EAGAIN.
static int cam_next(cam *c, int *used) {
	if (c->held >= 0) {
		struct v4l2_buffer b = {.index = c->held, .type = V4L2_BUF_TYPE_VIDEO_CAPTURE, .memory = V4L2_MEMORY_MMAP};
		c->held = -1;
		int r = xioctl(c->fd, VIDIOC_QBUF, &b);
		if (r < 0)
			return r;
	}
	struct pollfd p = {.fd = c->fd, .events = POLLIN};
	int r = poll(&p, 1, 100);
	if (r <= 0)
		return r == 0 || errno == EINTR ? -EAGAIN : -errno;
	struct v4l2_buffer b = {.type = V4L2_BUF_TYPE_VIDEO_CAPTURE, .memory = V4L2_MEMORY_MMAP};
	if ((r = xioctl(c->fd, VIDIOC_DQBUF, &b)) < 0)
		return r;
	c->held = b.index;
	*used = b.bytesused;
	return b.index;
}
*/
import "C"

import (
	"bytes"
	"image"
	"image/jpeg"
	"path/filepath"
	"syscall"
	"unsafe"

	"fyne.io/fyne/v2/canvas"
)

// The formats asked for, best first: those painters draw as they are, then
// MJPEG, which webcams send at higher frame rates than YUYV.
var formats = []C.uint32_t{C.V4L2_PIX_FMT_NV12, C.V4L2_PIX_FMT_YUV420, C.V4L2_PIX_FMT_MJPEG, C.V4L2_PIX_FMT_JPEG, C.V4L2_PIX_FMT_YUYV}

func devices() ([]Device, error) {
	paths, err := filepath.Glob("/dev/video*")
	var list []Device
	for _, path := range paths {
		var name [64]C.char
		p := C.CString(path)
		ok := C.capture(p, &name[0], C.int(len(name))) != 0
		C.free(unsafe.Pointer(p))
		if ok {
			list = append(list, Device{ID: path, Name: C.GoString(&name[0])})
		}
	}
	return list, err
}

func modes(id string) ([]mode, error) {
	var w, h [1024]C.uint32_t
	var fps [1024]C.double
	path := C.CString(id)
	defer C.free(unsafe.Pointer(path))
	n := C.cam_modes(path, &formats[0], C.int(len(formats)), &w[0], &h[0], &fps[0], C.int(len(w)))
	if n < 0 {
		return nil, syscall.Errno(-n)
	}
	return modeList(w[:n], h[:n], fps[:n]), nil
}

type stream struct {
	c       C.cam
	y, u, v []byte // YUYV converted to I420
}

func open(id string, m mode) (*stream, error) {
	s := new(stream)
	path := C.CString(id)
	defer C.free(unsafe.Pointer(path))
	r := C.cam_open(&s.c, path, &formats[0], C.int(len(formats)), C.int(m.Width), C.int(m.Height), C.double(m.fps))
	if r == -C.EPROTONOSUPPORT {
		return nil, errNoFormat
	}
	if r < 0 {
		return nil, syscall.Errno(-r)
	}
	return s, nil
}

func (s *stream) close() {
	C.cam_close(&s.c)
}

func (s *stream) next(p *picture) (bool, error) {
	var used C.int
	i := C.cam_next(&s.c, &used)
	if i == -C.EAGAIN {
		return false, nil
	}
	if i < 0 {
		return false, syscall.Errno(-i)
	}
	data := unsafe.Slice((*byte)(s.c.maps[i]), used)
	pix := &s.c.pix
	w, h, stride := int(pix.width), int(pix.height), int(pix.bytesperline)
	ch := (h + 1) / 2
	*p = picture{width: w, height: h, yStride: stride, cStride: stride}
	if pix.colorspace == C.V4L2_COLORSPACE_REC709 {
		p.colorSpace = canvas.VideoColorSpaceBT709
	}
	switch pix.pixelformat {
	case C.V4L2_PIX_FMT_NV12:
		if len(data) < stride*(h+ch) {
			return false, nil
		}
		p.y, p.uv = data[:stride*h], data[stride*h:]
	case C.V4L2_PIX_FMT_YUV420:
		cs := stride / 2
		if len(data) < stride*h+2*cs*ch {
			return false, nil
		}
		p.cStride = cs
		p.y, p.u, p.v = data[:stride*h], data[stride*h:stride*h+cs*ch], data[stride*h+cs*ch:]
	case C.V4L2_PIX_FMT_YUYV:
		if len(data) < stride*h {
			return false, nil
		}
		s.yuyv(p, data)
	default:
		img, err := jpeg.Decode(bytes.NewReader(data))
		yc, ok := img.(*image.YCbCr)
		if err != nil || !ok {
			return false, nil // a frame cut short; ponytail: grey JPEGs too
		}
		*p = picture{width: yc.Rect.Dx(), height: yc.Rect.Dy(), y: yc.Y, u: yc.Cb, v: yc.Cr, yStride: yc.YStride, cStride: yc.CStride, fullRange: true}
		switch yc.SubsampleRatio {
		case image.YCbCrSubsampleRatio420:
		case image.YCbCrSubsampleRatio422:
			p.cStride *= 2 // every other row of chroma makes it 4:2:0
		default:
			return false, nil // ponytail: 4:4:4 and the rest, which webcams don't send
		}
	}
	return true, nil
}

// yuyv converts the YUYV 4:2:2 picture in data to I420 in p.
func (s *stream) yuyv(p *picture, data []byte) {
	w, h, stride := p.width, p.height, p.yStride
	cw, ch := (w+1)/2, (h+1)/2
	s.y, s.u, s.v = grow(s.y, w*h), grow(s.u, cw*ch), grow(s.v, cw*ch)
	for r := range h {
		row, y := data[r*stride:], s.y[r*w:]
		for x := range w / 2 {
			y[2*x], y[2*x+1] = row[4*x], row[4*x+2]
		}
		if r%2 == 0 {
			u, v := s.u[r/2*cw:], s.v[r/2*cw:]
			for x := range w / 2 {
				u[x], v[x] = row[4*x+1], row[4*x+3]
			}
		}
	}
	p.y, p.u, p.v, p.yStride, p.cStride = s.y, s.u, s.v, w, cw
}
