package fcam

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AVFoundation -framework CoreMedia -framework CoreVideo -framework Foundation
#import <AVFoundation/AVFoundation.h>
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#include <sys/time.h>

// Sink keeps the newest picture of a capture session.
@interface FcamSink : NSObject <AVCaptureVideoDataOutputSampleBufferDelegate> {
@public
	pthread_mutex_t mu;
	pthread_cond_t cond;
	CVPixelBufferRef latest;
}
@end

@implementation FcamSink
- (instancetype)init {
	self = [super init];
	pthread_mutex_init(&mu, NULL);
	pthread_cond_init(&cond, NULL);
	return self;
}

- (void)dealloc {
	CVPixelBufferRelease(latest);
	pthread_cond_destroy(&cond);
	pthread_mutex_destroy(&mu);
}

- (void)captureOutput:(AVCaptureOutput *)output didOutputSampleBuffer:(CMSampleBufferRef)buffer fromConnection:(AVCaptureConnection *)connection {
	CVPixelBufferRef pb = CMSampleBufferGetImageBuffer(buffer);
	if (!pb)
		return;
	CVPixelBufferRetain(pb);
	pthread_mutex_lock(&mu);
	CVPixelBufferRelease(latest);
	latest = pb;
	pthread_cond_signal(&cond);
	pthread_mutex_unlock(&mu);
}
@end

typedef struct {
	void *session, *sink, *queue; // retained
	CVPixelBufferRef held;        // locked
	int front;

	uint8_t *y, *uv;
	size_t width, height, ystride, cstride;
	int bt709, full;
} avcam;

// av_devices lists the cameras as lines of "uniqueID\tname".
static char *av_devices(void) {
	@autoreleasepool {
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
		NSArray<AVCaptureDevice *> *devs = [AVCaptureDevice devicesWithMediaType:AVMediaTypeVideo];
#pragma clang diagnostic pop
		NSMutableString *s = [NSMutableString string];
		for (AVCaptureDevice *d in devs)
			[s appendFormat:@"%@\t%@\n", d.uniqueID, d.localizedName];
		return strdup(s.UTF8String);
	}
}

static const double common_rates[] = {60, 50, 30, 25, 24, 20, 15, 10, 5};

// av_modes puts up to n of the sizes and frame rates the camera with the
// given uniqueID sends at into w, h and fps, and returns how many, or -1 when
// there's no such camera. A range of frame rates counts as its ends and the
// common rates in between.
static int av_modes(const char *uid, int32_t *w, int32_t *h, double *fps, int n) {
	@autoreleasepool {
		AVCaptureDevice *dev = [AVCaptureDevice deviceWithUniqueID:@(uid)];
		if (!dev)
			return -1;
		int k = 0;
		for (AVCaptureDeviceFormat *f in dev.formats) {
			CMVideoDimensions d = CMVideoFormatDescriptionGetDimensions(f.formatDescription);
			for (AVFrameRateRange *r in f.videoSupportedFrameRateRanges) {
				double list[11];
				int m = 0;
				list[m++] = r.maxFrameRate;
				if (r.minFrameRate < r.maxFrameRate)
					list[m++] = r.minFrameRate;
				for (int j = 0; j < 9; j++)
					if (common_rates[j] > r.minFrameRate && common_rates[j] < r.maxFrameRate)
						list[m++] = common_rates[j];
				for (int j = 0; j < m && k < n; j++) {
					w[k] = d.width;
					h[k] = d.height;
					fps[k++] = list[j];
				}
			}
		}
		return k;
	}
}

// holding returns the frame rate range of f that holds fps, or its fastest
// with fps 0.
static AVFrameRateRange *holding(AVCaptureDeviceFormat *f, double fps) {
	AVFrameRateRange *best = nil;
	for (AVFrameRateRange *r in f.videoSupportedFrameRateRanges)
		if (fps > 0 ? fps > r.minFrameRate - 0.01 && fps < r.maxFrameRate + 0.01 : !best || r.maxFrameRate > best.maxFrameRate)
			best = r;
	return best;
}

// nearest returns the format of dev nearest width by height that runs at
// fps, or the fastest one with fps 0.
static AVCaptureDeviceFormat *nearest(AVCaptureDevice *dev, int width, int height, double fps) {
	AVCaptureDeviceFormat *best = nil;
	double bestScore = 0;
	for (AVCaptureDeviceFormat *f in dev.formats) {
		CMVideoDimensions d = CMVideoFormatDescriptionGetDimensions(f.formatDescription);
		AVFrameRateRange *r = holding(f, fps);
		double dw = d.width - width, dh = d.height - height;
		double score = (dw * dw + dh * dh) * 1000 + (fps > 0 ? (r ? 0 : 999) : 999 - MIN(r.maxFrameRate, 999));
		if (!best || score < bestScore) {
			best = f;
			bestScore = score;
		}
	}
	return best;
}

// allowed asks the user for the camera if they haven't been asked, and
// reports whether the app may use it. It blocks until they answer, so it
// must not run on the main thread.
static BOOL allowed(void) {
	AVAuthorizationStatus st = [AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeVideo];
	if (st != AVAuthorizationStatusNotDetermined)
		return st == AVAuthorizationStatusAuthorized;
	dispatch_semaphore_t done = dispatch_semaphore_create(0);
	__block BOOL ok = NO;
	[AVCaptureDevice requestAccessForMediaType:AVMediaTypeVideo completionHandler:^(BOOL granted) {
		ok = granted;
		dispatch_semaphore_signal(done);
	}];
	dispatch_semaphore_wait(done, DISPATCH_TIME_FOREVER);
	return ok;
}

static void av_close(avcam *c) {
	[(__bridge AVCaptureSession *)c->session stopRunning];
	dispatch_sync((__bridge dispatch_queue_t)c->queue, ^{}); // a picture on its way to the sink
	if (c->held) {
		CVPixelBufferUnlockBaseAddress(c->held, kCVPixelBufferLock_ReadOnly);
		CVPixelBufferRelease(c->held);
	}
	CFBridgingRelease(c->session);
	CFBridgingRelease(c->sink);
	CFBridgingRelease(c->queue);
}

// av_open starts the camera with the given uniqueID in its format nearest
// width by height, at fps pictures a second or the most there with fps 0, in
// NV12. It returns NULL or why it can't, to be freed.
static char *av_open(avcam *c, const char *uid, int width, int height, double fps) {
	@autoreleasepool {
		if (!allowed())
			return strdup("fcam: the user did not allow the camera");
		AVCaptureDevice *dev = [AVCaptureDevice deviceWithUniqueID:@(uid)];
		if (!dev)
			return strdup("fcam: no such camera");
		NSError *err = nil;
		AVCaptureDeviceInput *in = [AVCaptureDeviceInput deviceInputWithDevice:dev error:&err];
		if (!in)
			return strdup(err.localizedDescription.UTF8String);
		AVCaptureSession *s = [AVCaptureSession new];
		[s beginConfiguration];
		AVCaptureVideoDataOutput *out = [AVCaptureVideoDataOutput new];
		out.videoSettings = @{(id)kCVPixelBufferPixelFormatTypeKey: @(kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange)};
		out.alwaysDiscardsLateVideoFrames = YES;
		FcamSink *sink = [FcamSink new];
		dispatch_queue_t q = dispatch_queue_create("fcam", DISPATCH_QUEUE_SERIAL);
		[out setSampleBufferDelegate:sink queue:q];
		if (![s canAddInput:in] || ![s canAddOutput:out])
			return strdup("fcam: the camera can't be used");
		[s addInput:in];
		[s addOutput:out];
		AVCaptureDeviceFormat *f = nearest(dev, width, height, fps);
		if (f && [dev lockForConfiguration:nil]) {
			dev.activeFormat = f; // which makes the session's preset AVCaptureSessionPresetInputPriority
			AVFrameRateRange *r = holding(f, fps);
			if (r) {
				CMTime d = r.minFrameDuration;
				if (fps > 0 && fps < r.maxFrameRate - 0.01)
					d = CMTimeMake(1000, (int32_t)(fps * 1000 + 0.5));
				if (CMTimeCompare(d, r.maxFrameDuration) > 0) // outside the range throws
					d = r.maxFrameDuration;
				dev.activeVideoMinFrameDuration = d;
				dev.activeVideoMaxFrameDuration = d;
			}
			[dev unlockForConfiguration];
		}
		[s commitConfiguration];
		[s startRunning];
		c->session = (__bridge_retained void *)s;
		c->sink = (__bridge_retained void *)sink;
		c->queue = (__bridge_retained void *)q;
		c->front = dev.position == AVCaptureDevicePositionFront;
		return NULL;
	}
}

// av_next unlocks the picture the last call returned, then waits up to
// 100 ms for the next one and returns whether it came.
static int av_next(avcam *c) {
	if (c->held) {
		CVPixelBufferUnlockBaseAddress(c->held, kCVPixelBufferLock_ReadOnly);
		CVPixelBufferRelease(c->held);
		c->held = NULL;
	}
	FcamSink *sink = (__bridge FcamSink *)c->sink;
	pthread_mutex_lock(&sink->mu);
	if (!sink->latest) {
		struct timeval now;
		gettimeofday(&now, NULL);
		long ns = now.tv_usec * 1000 + 100000000;
		struct timespec until = {now.tv_sec + ns / 1000000000, ns % 1000000000};
		pthread_cond_timedwait(&sink->cond, &sink->mu, &until);
	}
	CVPixelBufferRef pb = sink->latest;
	sink->latest = NULL;
	pthread_mutex_unlock(&sink->mu);
	if (!pb)
		return 0;
	CVPixelBufferLockBaseAddress(pb, kCVPixelBufferLock_ReadOnly);
	c->held = pb;
	c->width = CVPixelBufferGetWidth(pb);
	c->height = CVPixelBufferGetHeight(pb);
	c->y = CVPixelBufferGetBaseAddressOfPlane(pb, 0);
	c->ystride = CVPixelBufferGetBytesPerRowOfPlane(pb, 0);
	c->uv = CVPixelBufferGetBaseAddressOfPlane(pb, 1);
	c->cstride = CVPixelBufferGetBytesPerRowOfPlane(pb, 1);
	c->full = CVPixelBufferGetPixelFormatType(pb) == kCVPixelFormatType_420YpCbCr8BiPlanarFullRange;
#pragma clang diagnostic push
#pragma clang diagnostic ignored "-Wdeprecated-declarations"
	CFTypeRef m = CVBufferGetAttachment(pb, kCVImageBufferYCbCrMatrixKey, NULL);
#pragma clang diagnostic pop
	c->bt709 = m && CFEqual(m, kCVImageBufferYCbCrMatrix_ITU_R_709_2);
	return c->y && c->uv;
}
*/
import "C"

import (
	"errors"
	"runtime"
	"unsafe"

	"fyne.io/fyne/v2/canvas"
)

func devices() ([]Device, error) {
	s := C.av_devices()
	defer C.free(unsafe.Pointer(s))
	return parseDevices(C.GoString(s)), nil
}

func modes(id string) ([]mode, error) {
	var w, h [1024]C.int32_t
	var fps [1024]C.double
	uid := C.CString(id)
	defer C.free(unsafe.Pointer(uid))
	n := C.av_modes(uid, &w[0], &h[0], &fps[0], C.int(len(w)))
	if n < 0 {
		return nil, errors.New("fcam: no such camera")
	}
	return modeList(w[:n], h[:n], fps[:n]), nil
}

type stream struct {
	c C.avcam
}

func open(id string, m mode) (*stream, error) {
	s := new(stream)
	uid := C.CString(id)
	defer C.free(unsafe.Pointer(uid))
	if err := C.av_open(&s.c, uid, C.int(m.Width), C.int(m.Height), C.double(m.fps)); err != nil {
		defer C.free(unsafe.Pointer(err))
		return nil, errors.New(C.GoString(err))
	}
	return s, nil
}

func (s *stream) close() {
	C.av_close(&s.c)
}

func (s *stream) next(p *picture) (bool, error) {
	if C.av_next(&s.c) == 0 {
		return false, nil
	}
	c := &s.c
	w, h, ys, cs := int(c.width), int(c.height), int(c.ystride), int(c.cstride)
	*p = picture{width: w, height: h,
		y: unsafe.Slice((*byte)(c.y), ys*h), uv: unsafe.Slice((*byte)(c.uv), cs*((h+1)/2)),
		yStride: ys, cStride: cs, fullRange: c.full != 0,
		front: c.front != 0}
	if runtime.GOOS == "ios" {
		p.rotation = 90 // iOS cameras send pictures as for a phone turned left
	}
	if c.bt709 != 0 {
		p.colorSpace = canvas.VideoColorSpaceBT709
	}
	return true, nil
}
