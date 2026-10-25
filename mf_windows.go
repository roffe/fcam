package fcam

/*
#cgo LDFLAGS: -lmfplat -lmf -lmfreadwrite -lmfuuid -lole32
#define COBJMACROS
#include <windows.h>
#include <mfapi.h>
#include <mfidl.h>
#include <mfreadwrite.h>
#include <mferror.h>
#include <stdio.h>
#include <stdlib.h>

typedef struct {
	IMFMediaSource *source;
	IMFSourceReader *reader;
	IMFMediaBuffer *held; // locked
	IMF2DBuffer2 *held2d; // the same, when it has rows
	UINT32 width, height, matrix, range;
	LONG stride;
	int uninit;
} mfcam;

static int mf_startup(void) {
	HRESULT hr = CoInitializeEx(NULL, COINIT_MULTITHREADED);
	int uninit = SUCCEEDED(hr);
	if (FAILED(MFStartup(MF_VERSION, MFSTARTUP_NOSOCKET)) && uninit) {
		CoUninitialize();
		return -1;
	}
	return uninit;
}

static void mf_shutdown(int uninit) {
	MFShutdown();
	if (uninit)
		CoUninitialize();
}

static void utf8(char *dst, int size, const wchar_t *src) {
	if (!WideCharToMultiByte(CP_UTF8, 0, src, -1, dst, size, NULL, NULL))
		dst[0] = 0;
}

static HRESULT video_attributes(IMFAttributes **a) {
	HRESULT hr = MFCreateAttributes(a, 2);
	if (SUCCEEDED(hr))
		hr = IMFAttributes_SetGUID(*a, &MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE, &MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_GUID);
	return hr;
}

// mf_devices lists the video capture devices as lines of "symbolic link\tname".
static char *mf_devices(HRESULT *res) {
	int uninit = mf_startup();
	if (uninit < 0) {
		*res = E_FAIL;
		return NULL;
	}
	IMFAttributes *a = NULL;
	IMFActivate **devs = NULL;
	UINT32 n = 0;
	HRESULT hr = video_attributes(&a);
	if (SUCCEEDED(hr))
		hr = MFEnumDeviceSources(a, &devs, &n);
	size_t size = 512 * (n + 1), len = 0;
	char *out = calloc(1, size);
	for (UINT32 i = 0; i < n; i++) {
		WCHAR *name = NULL, *link = NULL;
		UINT32 l;
		if (SUCCEEDED(IMFActivate_GetAllocatedString(devs[i], &MF_DEVSOURCE_ATTRIBUTE_FRIENDLY_NAME, &name, &l)) &&
			SUCCEEDED(IMFActivate_GetAllocatedString(devs[i], &MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_SYMBOLIC_LINK, &link, &l))) {
			char n8[128], l8[384];
			utf8(n8, sizeof n8, name);
			utf8(l8, sizeof l8, link);
			len += snprintf(out + len, size - len, "%s\t%s\n", l8, n8);
		}
		CoTaskMemFree(name);
		CoTaskMemFree(link);
		IMFActivate_Release(devs[i]);
	}
	CoTaskMemFree(devs);
	if (a)
		IMFAttributes_Release(a);
	mf_shutdown(uninit);
	*res = hr;
	return out;
}

static void release(mfcam *c) {
	if (c->held2d) {
		IMF2DBuffer2_Unlock2D(c->held2d);
		IMF2DBuffer2_Release(c->held2d);
	} else if (c->held)
		IMFMediaBuffer_Unlock(c->held);
	if (c->held)
		IMFMediaBuffer_Release(c->held);
	c->held = NULL;
	c->held2d = NULL;
}

static void mf_close(mfcam *c) {
	release(c);
	if (c->reader)
		IMFSourceReader_Release(c->reader);
	if (c->source) {
		IMFMediaSource_Shutdown(c->source);
		IMFMediaSource_Release(c->source);
	}
	if (c->uninit >= 0)
		mf_shutdown(c->uninit);
}

// output reads the size and colours of the pictures the reader gives.
static HRESULT output(mfcam *c) {
	IMFMediaType *t;
	HRESULT hr = IMFSourceReader_GetCurrentMediaType(c->reader, MF_SOURCE_READER_FIRST_VIDEO_STREAM, &t);
	if (FAILED(hr))
		return hr;
	UINT64 size = 0;
	UINT32 stride = 0;
	hr = IMFMediaType_GetUINT64(t, &MF_MT_FRAME_SIZE, &size);
	c->width = size >> 32;
	c->height = (UINT32)size;
	if (FAILED(IMFMediaType_GetUINT32(t, &MF_MT_DEFAULT_STRIDE, &stride)))
		stride = c->width;
	c->stride = (LONG)stride;
	if (FAILED(IMFMediaType_GetUINT32(t, &MF_MT_YUV_MATRIX, &c->matrix)))
		c->matrix = MFVideoTransferMatrix_BT601;
	if (FAILED(IMFMediaType_GetUINT32(t, &MF_MT_VIDEO_NOMINAL_RANGE, &c->range)))
		c->range = MFNominalRange_16_235;
	IMFMediaType_Release(t);
	return hr;
}

// mf_reader makes a source reader for the device with the symbolic link
// link, to be closed with mf_close whether it works or not.
static HRESULT mf_reader(mfcam *c, const char *link) {
	c->uninit = mf_startup();
	if (c->uninit < 0)
		return E_FAIL;
	WCHAR wlink[512];
	if (!MultiByteToWideChar(CP_UTF8, 0, link, -1, wlink, 512))
		return E_INVALIDARG;
	IMFAttributes *a = NULL, *ra = NULL;
	HRESULT hr = video_attributes(&a);
	if (SUCCEEDED(hr))
		hr = IMFAttributes_SetString(a, &MF_DEVSOURCE_ATTRIBUTE_SOURCE_TYPE_VIDCAP_SYMBOLIC_LINK, wlink);
	if (SUCCEEDED(hr))
		hr = MFCreateDeviceSource(a, &c->source);
	if (SUCCEEDED(hr))
		hr = MFCreateAttributes(&ra, 1);
	if (SUCCEEDED(hr))
		hr = IMFAttributes_SetUINT32(ra, &MF_SOURCE_READER_ENABLE_ADVANCED_VIDEO_PROCESSING, TRUE);
	if (SUCCEEDED(hr))
		hr = MFCreateSourceReaderFromMediaSource(c->source, ra, &c->reader);
	if (a)
		IMFAttributes_Release(a);
	if (ra)
		IMFAttributes_Release(ra);
	return hr;
}

static double rate(UINT64 r) {
	return (UINT32)r ? (double)(r >> 32) / (UINT32)r : 0;
}

// rates puts the frame rate of t in f and the range it can be set in in lo and hi.
static void rates(IMFMediaType *t, double *f, double *lo, double *hi) {
	UINT64 r = 0;
	IMFMediaType_GetUINT64(t, &MF_MT_FRAME_RATE, &r);
	*f = *lo = *hi = rate(r);
	if (SUCCEEDED(IMFMediaType_GetUINT64(t, &MF_MT_FRAME_RATE_RANGE_MIN, &r)) && rate(r) > 0 && rate(r) < *lo)
		*lo = rate(r);
	if (SUCCEEDED(IMFMediaType_GetUINT64(t, &MF_MT_FRAME_RATE_RANGE_MAX, &r)) && rate(r) > *hi)
		*hi = rate(r);
}

static const double common_rates[] = {60, 50, 30, 25, 24, 20, 15, 10, 5};

// mf_modes puts up to n of the sizes and frame rates the device with the
// symbolic link link sends at into w, h and fps, and returns how many. A
// range of frame rates counts as its ends and the common rates in between.
static int mf_modes(const char *link, UINT32 *w, UINT32 *h, double *fps, int n, HRESULT *res) {
	mfcam c = {0};
	HRESULT hr = mf_reader(&c, link);
	IMFMediaType *t;
	int k = 0;
	for (DWORD i = 0; SUCCEEDED(hr) && k < n && SUCCEEDED(IMFSourceReader_GetNativeMediaType(c.reader, MF_SOURCE_READER_FIRST_VIDEO_STREAM, i, &t)); i++) {
		UINT64 size = 0;
		double f, lo, hi, list[11];
		rates(t, &f, &lo, &hi);
		int m = 0;
		list[m++] = hi;
		if (lo < hi)
			list[m++] = lo;
		for (int j = 0; j < 9; j++)
			if (common_rates[j] > lo && common_rates[j] < hi)
				list[m++] = common_rates[j];
		for (int j = 0; j < m && k < n && SUCCEEDED(IMFMediaType_GetUINT64(t, &MF_MT_FRAME_SIZE, &size)); j++) {
			w[k] = size >> 32;
			h[k] = (UINT32)size;
			fps[k++] = list[j];
		}
		IMFMediaType_Release(t);
	}
	mf_close(&c);
	*res = hr;
	return k;
}

// mf_open starts the device with the symbolic link link in the mode nearest
// width by height and fps pictures a second, or the most there with fps 0,
// converted to NV12.
static HRESULT mf_open(mfcam *c, const char *link, UINT32 width, UINT32 height, double fps) {
	HRESULT hr = mf_reader(c, link);
	IMFMediaType *t, *best = NULL;
	double bestScore = 0;
	for (DWORD i = 0; SUCCEEDED(hr) && SUCCEEDED(IMFSourceReader_GetNativeMediaType(c->reader, MF_SOURCE_READER_FIRST_VIDEO_STREAM, i, &t)); i++) {
		UINT64 size = 0;
		double f, lo, hi;
		IMFMediaType_GetUINT64(t, &MF_MT_FRAME_SIZE, &size);
		rates(t, &f, &lo, &hi);
		double dw = (double)(size >> 32) - width, dh = (double)(UINT32)size - height;
		double miss = fps < lo ? lo - fps : fps > hi ? fps - hi : 0;
		double score = (dw * dw + dh * dh) * 1000 + (fps > 0 ? miss : 999 - (hi > 999 ? 999 : hi));
		if (!best || score < bestScore) {
			if (best)
				IMFMediaType_Release(best);
			best = t;
			bestScore = score;
		} else
			IMFMediaType_Release(t);
	}
	if (best) {
		double f, lo, hi, want = fps > 0 ? fps : 1e9;
		rates(best, &f, &lo, &hi);
		want = want < lo ? lo : want > hi ? hi : want;
		if (want != f)
			IMFMediaType_SetUINT64(best, &MF_MT_FRAME_RATE, (UINT64)(want * 1000 + 0.5) << 32 | 1000);
		if (SUCCEEDED(hr))
			hr = IMFSourceReader_SetCurrentMediaType(c->reader, MF_SOURCE_READER_FIRST_VIDEO_STREAM, NULL, best);
		IMFMediaType_Release(best);
	}

	IMFMediaType *nv12 = NULL;
	if (SUCCEEDED(hr))
		hr = MFCreateMediaType(&nv12);
	if (SUCCEEDED(hr))
		hr = IMFMediaType_SetGUID(nv12, &MF_MT_MAJOR_TYPE, &MFMediaType_Video);
	if (SUCCEEDED(hr))
		hr = IMFMediaType_SetGUID(nv12, &MF_MT_SUBTYPE, &MFVideoFormat_NV12);
	if (SUCCEEDED(hr))
		hr = IMFSourceReader_SetCurrentMediaType(c->reader, MF_SOURCE_READER_FIRST_VIDEO_STREAM, NULL, nv12);
	if (nv12)
		IMFMediaType_Release(nv12);
	if (SUCCEEDED(hr))
		hr = output(c);
	if (FAILED(hr))
		mf_close(c);
	return hr;
}

// mf_next unlocks the picture the last call returned, then reads the next
// one into data, of len bytes with rows pitch apart. It returns S_FALSE when
// the reader gave none.
static HRESULT mf_next(mfcam *c, BYTE **data, LONG *pitch, DWORD *len) {
	release(c);

	DWORD flags = 0;
	LONGLONG ts;
	IMFSample *s = NULL;
	HRESULT hr = IMFSourceReader_ReadSample(c->reader, MF_SOURCE_READER_FIRST_VIDEO_STREAM, 0, NULL, &flags, &ts, &s);
	if (SUCCEEDED(hr) && (flags & (MF_SOURCE_READERF_ERROR | MF_SOURCE_READERF_ENDOFSTREAM)))
		hr = MF_E_END_OF_STREAM;
	if (SUCCEEDED(hr) && (flags & MF_SOURCE_READERF_CURRENTMEDIATYPECHANGED))
		hr = output(c);
	if (FAILED(hr) || !s) {
		if (s)
			IMFSample_Release(s);
		return FAILED(hr) ? hr : S_FALSE;
	}
	hr = IMFSample_ConvertToContiguousBuffer(s, &c->held);
	IMFSample_Release(s);
	if (FAILED(hr))
		return hr;
	BYTE *start;
	if (SUCCEEDED(IMFMediaBuffer_QueryInterface(c->held, &IID_IMF2DBuffer2, (void **)&c->held2d))) {
		hr = IMF2DBuffer2_Lock2DSize(c->held2d, MF2DBuffer_LockFlags_Read, data, pitch, &start, len);
		if (SUCCEEDED(hr))
			*len -= *data - start;
		else {
			IMF2DBuffer2_Release(c->held2d);
			c->held2d = NULL;
		}
	} else
		hr = E_NOINTERFACE;
	if (FAILED(hr)) {
		*pitch = c->stride;
		hr = IMFMediaBuffer_Lock(c->held, data, NULL, len);
	}
	if (FAILED(hr)) {
		IMFMediaBuffer_Release(c->held);
		c->held = NULL;
	}
	return hr;
}
*/
import "C"

import (
	"fmt"
	"unsafe"

	"fyne.io/fyne/v2/canvas"
)

type hresult C.HRESULT

func (h hresult) Error() string {
	return fmt.Sprintf("fcam: Media Foundation error 0x%08x", uint32(h))
}

func devices() ([]Device, error) {
	var hr C.HRESULT
	s := C.mf_devices(&hr)
	defer C.free(unsafe.Pointer(s))
	if hr < 0 {
		return nil, hresult(hr)
	}
	return parseDevices(C.GoString(s)), nil
}

func modes(id string) ([]mode, error) {
	var w, h [1024]C.UINT32
	var fps [1024]C.double
	var hr C.HRESULT
	link := C.CString(id)
	defer C.free(unsafe.Pointer(link))
	n := C.mf_modes(link, &w[0], &h[0], &fps[0], C.int(len(w)), &hr)
	if hr < 0 {
		return nil, hresult(hr)
	}
	return modeList(w[:n], h[:n], fps[:n]), nil
}

type stream struct {
	c C.mfcam
}

func open(id string, m mode) (*stream, error) {
	s := new(stream)
	link := C.CString(id)
	defer C.free(unsafe.Pointer(link))
	if hr := C.mf_open(&s.c, link, C.UINT32(m.Width), C.UINT32(m.Height), C.double(m.fps)); hr < 0 {
		return nil, hresult(hr)
	}
	return s, nil
}

func (s *stream) close() {
	C.mf_close(&s.c)
}

func (s *stream) next(p *picture) (bool, error) {
	var data *C.BYTE
	var pitch C.LONG
	var size C.DWORD
	hr := C.mf_next(&s.c, &data, &pitch, &size)
	if hr < 0 {
		return false, hresult(hr)
	}
	w, h, stride := int(s.c.width), int(s.c.height), int(pitch)
	ch := (h + 1) / 2
	if hr != 0 || stride < w || int(size) < stride*(h+ch) {
		return false, nil
	}
	b := unsafe.Slice((*byte)(data), stride*(h+ch))
	*p = picture{width: w, height: h, y: b[:stride*h], uv: b[stride*h:], yStride: stride, cStride: stride,
		fullRange: s.c._range == C.MFNominalRange_0_255}
	switch s.c.matrix {
	case C.MFVideoTransferMatrix_BT709:
		p.colorSpace = canvas.VideoColorSpaceBT709
	case C.MFVideoTransferMatrix_BT2020_10, C.MFVideoTransferMatrix_BT2020_12:
		p.colorSpace = canvas.VideoColorSpaceBT2020
	}
	return true, nil
}
