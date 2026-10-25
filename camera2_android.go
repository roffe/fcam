package fcam

/*
#cgo CFLAGS: -D__ANDROID_UNAVAILABLE_SYMBOLS_ARE_WEAK__ -Wno-unguarded-availability
#cgo LDFLAGS: -ldl
#include <camera/NdkCameraCaptureSession.h>
#include <camera/NdkCameraDevice.h>
#include <camera/NdkCameraManager.h>
#include <camera/NdkCameraMetadata.h>
#include <camera/NdkCaptureRequest.h>
#include <dlfcn.h>
#include <jni.h>
#include <media/NdkImageReader.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

// The camera and media NDK libraries came with Android 7, later than the
// Android fyne builds for, so their functions are looked up when first used.
#define CAMERA_FUNCS(X) \
	X(ACameraManager_create) X(ACameraManager_delete) X(ACameraManager_getCameraIdList) \
	X(ACameraManager_deleteCameraIdList) X(ACameraManager_getCameraCharacteristics) \
	X(ACameraManager_openCamera) X(ACameraMetadata_getConstEntry) X(ACameraMetadata_free) \
	X(ACameraDevice_close) X(ACameraDevice_createCaptureRequest) X(ACameraDevice_createCaptureSession) \
	X(ACaptureRequest_addTarget) X(ACaptureRequest_setEntry_i32) X(ACaptureRequest_free) X(ACameraOutputTarget_create) \
	X(ACameraOutputTarget_free) X(ACaptureSessionOutputContainer_create) \
	X(ACaptureSessionOutputContainer_free) X(ACaptureSessionOutputContainer_add) \
	X(ACaptureSessionOutput_create) X(ACaptureSessionOutput_free) \
	X(ACameraCaptureSession_setRepeatingRequest) X(ACameraCaptureSession_close)
#define MEDIA_FUNCS(X) \
	X(AImageReader_new) X(AImageReader_delete) X(AImageReader_getWindow) \
	X(AImageReader_setImageListener) X(AImageReader_acquireLatestImage) X(AImage_delete) \
	X(AImage_getWidth) X(AImage_getHeight) X(AImage_getPlaneData) \
	X(AImage_getPlaneRowStride) X(AImage_getPlanePixelStride)

#define DECLARE(f) static __typeof__(f) *p_##f;
CAMERA_FUNCS(DECLARE)
MEDIA_FUNCS(DECLARE)

static int loaded;

static void load_once(void) {
	void *camera = dlopen("libcamera2ndk.so", RTLD_NOW), *media = dlopen("libmediandk.so", RTLD_NOW);
	if (!camera || !media)
		return;
#define LOAD_CAMERA(f) if (!(p_##f = dlsym(camera, #f))) return;
#define LOAD_MEDIA(f) if (!(p_##f = dlsym(media, #f))) return;
	CAMERA_FUNCS(LOAD_CAMERA)
	MEDIA_FUNCS(LOAD_MEDIA)
	loaded = 1;
}

static int load(void) {
	static pthread_once_t once = PTHREAD_ONCE_INIT;
	pthread_once(&once, load_once);
	return loaded;
}

// describe returns which way the camera id faces, and puts how its sensor
// is turned in sensor and the YUV size nearest w by h it gives in w and h.
static int describe(ACameraManager *m, const char *id, int *sensor, int *w, int *h) {
	ACameraMetadata *md;
	if (p_ACameraManager_getCameraCharacteristics(m, id, &md) != ACAMERA_OK)
		return -1;
	ACameraMetadata_const_entry e;
	int facing = -1;
	if (p_ACameraMetadata_getConstEntry(md, ACAMERA_LENS_FACING, &e) == ACAMERA_OK && e.count)
		facing = e.data.u8[0];
	if (sensor && p_ACameraMetadata_getConstEntry(md, ACAMERA_SENSOR_ORIENTATION, &e) == ACAMERA_OK && e.count)
		*sensor = e.data.i32[0];
	if (w && p_ACameraMetadata_getConstEntry(md, ACAMERA_SCALER_AVAILABLE_STREAM_CONFIGURATIONS, &e) == ACAMERA_OK) {
		long best = -1, ww = *w, wh = *h;
		for (uint32_t i = 0; i + 3 < e.count; i += 4) {
			const int32_t *c = e.data.i32 + i; // format, width, height, input
			long dw = c[1] - ww, dh = c[2] - wh;
			if (c[0] == AIMAGE_FORMAT_YUV_420_888 && !c[3] && (best < 0 || dw * dw + dh * dh < best)) {
				best = dw * dw + dh * dh;
				*w = c[1];
				*h = c[2];
			}
		}
	}
	p_ACameraMetadata_free(md);
	return facing;
}

// top_rate returns the most pictures a second the camera of md sends YUV
// at w by h, or 0 when it doesn't say.
static double top_rate(const ACameraMetadata *md, int32_t w, int32_t h) {
	ACameraMetadata_const_entry e;
	if (p_ACameraMetadata_getConstEntry(md, ACAMERA_SCALER_AVAILABLE_MIN_FRAME_DURATIONS, &e) == ACAMERA_OK)
		for (uint32_t i = 0; i + 3 < e.count; i += 4) {
			const int64_t *d = e.data.i64 + i; // format, width, height, nanoseconds
			if (d[0] == AIMAGE_FORMAT_YUV_420_888 && d[1] == w && d[2] == h && d[3] > 0)
				return 1e9 / d[3];
		}
	return 0;
}

// ac_modes puts up to n of the YUV sizes camera id gives, and the frame rates
// its auto exposure runs at there, into w, h and fps, and returns how many,
// or -1 when there's no such camera.
static int ac_modes(const char *id, int32_t *w, int32_t *h, double *fps, int n) {
	if (!load())
		return -1;
	ACameraManager *m = p_ACameraManager_create();
	ACameraMetadata *md;
	int k = -1;
	if (p_ACameraManager_getCameraCharacteristics(m, id, &md) == ACAMERA_OK) {
		ACameraMetadata_const_entry e, ae = {0};
		p_ACameraMetadata_getConstEntry(md, ACAMERA_CONTROL_AE_AVAILABLE_TARGET_FPS_RANGES, &ae);
		k = 0;
		if (p_ACameraMetadata_getConstEntry(md, ACAMERA_SCALER_AVAILABLE_STREAM_CONFIGURATIONS, &e) == ACAMERA_OK)
			for (uint32_t i = 0; i + 3 < e.count; i += 4) {
				const int32_t *c = e.data.i32 + i; // format, width, height, input
				if (c[0] != AIMAGE_FORMAT_YUV_420_888 || c[3])
					continue;
				double top = top_rate(md, c[1], c[2]);
				int first = k;
				for (uint32_t j = 0; j + 1 < ae.count && k < n; j += 2) // ranges of min, max
					if (top == 0 || ae.data.i32[j + 1] <= top + 0.5) {
						w[k] = c[1];
						h[k] = c[2];
						fps[k++] = ae.data.i32[j + 1];
					}
				if (k == first && k < n) {
					w[k] = c[1];
					h[k] = c[2];
					fps[k++] = top;
				}
			}
		p_ACameraMetadata_free(md);
	}
	p_ACameraManager_delete(m);
	return k;
}

// fps_range puts the auto exposure frame rate range of camera id that tops
// out at fps, or the highest one w by h takes with fps 0, into r, a fixed one
// where it has one. It reports whether it found one.
static int fps_range(ACameraManager *m, const char *id, int32_t w, int32_t h, double fps, int32_t r[2]) {
	ACameraMetadata *md;
	if (p_ACameraManager_getCameraCharacteristics(m, id, &md) != ACAMERA_OK)
		return 0;
	ACameraMetadata_const_entry e;
	double top = top_rate(md, w, h);
	int found = 0;
	if (p_ACameraMetadata_getConstEntry(md, ACAMERA_CONTROL_AE_AVAILABLE_TARGET_FPS_RANGES, &e) == ACAMERA_OK)
		for (uint32_t i = 0; i + 1 < e.count; i += 2) {
			int32_t lo = e.data.i32[i], hi = e.data.i32[i + 1];
			if (fps > 0 ? hi < fps - 0.5 || hi > fps + 0.5 : top > 0 && hi > top + 0.5)
				continue;
			if (!found || hi > r[1] || (hi == r[1] && lo > r[0])) {
				r[0] = lo;
				r[1] = hi;
				found = 1;
			}
		}
	p_ACameraMetadata_free(md);
	return found;
}

// ac_devices lists the cameras as lines of "id\tname".
static char *ac_devices(void) {
	if (!load())
		return NULL;
	ACameraManager *m = p_ACameraManager_create();
	ACameraIdList *ids = NULL;
	char buf[4096] = "";
	size_t n = 0;
	if (p_ACameraManager_getCameraIdList(m, &ids) == ACAMERA_OK) {
		for (int i = 0; i < ids->numCameras && n < sizeof buf - 128; i++) {
			const char *id = ids->cameraIds[i];
			int f = describe(m, id, NULL, NULL, NULL);
			const char *kind = f == ACAMERA_LENS_FACING_FRONT ? "Front" : f == ACAMERA_LENS_FACING_BACK ? "Back" : "External";
			n += snprintf(buf + n, sizeof buf - n, "%s\t%s camera %s\n", id, kind, id);
		}
		p_ACameraManager_deleteCameraIdList(ids);
	}
	p_ACameraManager_delete(m);
	return strdup(buf);
}

typedef struct {
	ACameraManager *mgr;
	ACameraDevice *dev;
	AImageReader *reader;
	ACaptureSessionOutputContainer *outputs;
	ACaptureSessionOutput *output;
	ACameraOutputTarget *target;
	ACaptureRequest *req;
	ACameraCaptureSession *session;
	ACameraDevice_StateCallbacks devcb;
	ACameraCaptureSession_stateCallbacks sesscb;
	AImageReader_ImageListener listener;
	pthread_mutex_t mu;
	pthread_cond_t cond;
	int ready, lost, sensor, front;

	AImage *held;
	int32_t width, height, ystride, cstride, cpixel;
	uint8_t *y, *u, *v;
	int ylen, ulen, vlen;
} acam;

static void on_image(void *ctx, AImageReader *r) {
	acam *c = ctx;
	pthread_mutex_lock(&c->mu);
	c->ready = 1;
	pthread_cond_signal(&c->cond);
	pthread_mutex_unlock(&c->mu);
}

static void on_lost(void *ctx, ACameraDevice *d) {
	acam *c = ctx;
	pthread_mutex_lock(&c->mu);
	c->lost = 1;
	pthread_cond_signal(&c->cond);
	pthread_mutex_unlock(&c->mu);
}

static void on_error(void *ctx, ACameraDevice *d, int err) {
	on_lost(ctx, d);
}

static void on_session(void *ctx, ACameraCaptureSession *s) {
}

static void ac_close(acam *c) {
	if (c->session)
		p_ACameraCaptureSession_close(c->session);
	if (c->dev)
		p_ACameraDevice_close(c->dev);
	if (c->held)
		p_AImage_delete(c->held);
	if (c->reader)
		p_AImageReader_delete(c->reader);
	if (c->req)
		p_ACaptureRequest_free(c->req);
	if (c->target)
		p_ACameraOutputTarget_free(c->target);
	if (c->output)
		p_ACaptureSessionOutput_free(c->output);
	if (c->outputs)
		p_ACaptureSessionOutputContainer_free(c->outputs);
	if (c->mgr)
		p_ACameraManager_delete(c->mgr);
	pthread_cond_destroy(&c->cond);
	pthread_mutex_destroy(&c->mu);
}

// ac_open starts the camera id at the YUV size nearest width by height, at
// fps pictures a second or the most it sends with fps 0, returning a
// camera_status_t or media_status_t error.
static int ac_open(acam *c, const char *id, int width, int height, double fps) {
	if (!load())
		return ACAMERA_ERROR_UNSUPPORTED_OPERATION;
	pthread_mutex_init(&c->mu, NULL);
	pthread_cond_init(&c->cond, NULL);
	c->mgr = p_ACameraManager_create();
	c->front = describe(c->mgr, id, &c->sensor, &width, &height) == ACAMERA_LENS_FACING_FRONT;
	c->devcb = (ACameraDevice_StateCallbacks){.context = c, .onDisconnected = on_lost, .onError = on_error};
	c->sesscb = (ACameraCaptureSession_stateCallbacks){.context = c, .onClosed = on_session, .onReady = on_session, .onActive = on_session};
	c->listener = (AImageReader_ImageListener){.context = c, .onImageAvailable = on_image};
	ANativeWindow *win = NULL;
	int r = p_ACameraManager_openCamera(c->mgr, id, &c->devcb, &c->dev);
	if (r == ACAMERA_OK)
		r = p_AImageReader_new(width, height, AIMAGE_FORMAT_YUV_420_888, 4, &c->reader);
	if (r == ACAMERA_OK)
		r = p_AImageReader_setImageListener(c->reader, &c->listener);
	if (r == ACAMERA_OK)
		r = p_AImageReader_getWindow(c->reader, &win);
	if (r == ACAMERA_OK)
		r = p_ACaptureSessionOutputContainer_create(&c->outputs);
	if (r == ACAMERA_OK)
		r = p_ACaptureSessionOutput_create(win, &c->output);
	if (r == ACAMERA_OK)
		r = p_ACaptureSessionOutputContainer_add(c->outputs, c->output);
	if (r == ACAMERA_OK)
		r = p_ACameraOutputTarget_create(win, &c->target);
	if (r == ACAMERA_OK)
		r = p_ACameraDevice_createCaptureRequest(c->dev, TEMPLATE_PREVIEW, &c->req);
	int32_t rates[2];
	if (r == ACAMERA_OK && fps_range(c->mgr, id, width, height, fps, rates))
		r = p_ACaptureRequest_setEntry_i32(c->req, ACAMERA_CONTROL_AE_TARGET_FPS_RANGE, 2, rates);
	if (r == ACAMERA_OK)
		r = p_ACaptureRequest_addTarget(c->req, c->target);
	if (r == ACAMERA_OK)
		r = p_ACameraDevice_createCaptureSession(c->dev, c->outputs, &c->sesscb, &c->session);
	if (r == ACAMERA_OK)
		r = p_ACameraCaptureSession_setRepeatingRequest(c->session, NULL, 1, &c->req, NULL);
	if (r != ACAMERA_OK)
		ac_close(c);
	return r;
}

// ac_next gives the picture the last call returned back to the reader, then
// waits up to 100 ms for the next one. It returns 1 when it came, 0 when it
// didn't, and -1 when the camera is gone.
static int ac_next(acam *c) {
	if (c->held) {
		p_AImage_delete(c->held);
		c->held = NULL;
	}
	pthread_mutex_lock(&c->mu);
	if (!c->ready && !c->lost) {
		struct timespec until;
		clock_gettime(CLOCK_REALTIME, &until);
		until.tv_nsec += 100000000;
		until.tv_sec += until.tv_nsec / 1000000000;
		until.tv_nsec %= 1000000000;
		pthread_cond_timedwait(&c->cond, &c->mu, &until);
	}
	int lost = c->lost;
	c->ready = 0;
	pthread_mutex_unlock(&c->mu);
	if (lost)
		return -1;
	if (p_AImageReader_acquireLatestImage(c->reader, &c->held) != AMEDIA_OK || !c->held) {
		c->held = NULL;
		return 0;
	}
	AImage *img = c->held;
	p_AImage_getWidth(img, &c->width);
	p_AImage_getHeight(img, &c->height);
	p_AImage_getPlaneData(img, 0, &c->y, &c->ylen);
	p_AImage_getPlaneRowStride(img, 0, &c->ystride);
	p_AImage_getPlaneData(img, 1, &c->u, &c->ulen);
	p_AImage_getPlaneData(img, 2, &c->v, &c->vlen);
	p_AImage_getPlaneRowStride(img, 1, &c->cstride);
	p_AImage_getPlanePixelStride(img, 1, &c->cpixel);
	return 1;
}

// call calls o's method name, of signature sig, with no arguments.
static jobject call(JNIEnv *e, jobject o, const char *name, const char *sig) {
	if (!o)
		return NULL;
	jclass cls = (*e)->GetObjectClass(e, o);
	jmethodID m = (*e)->GetMethodID(e, cls, name, sig);
	(*e)->DeleteLocalRef(e, cls);
	jobject r = m ? (*e)->CallObjectMethod(e, o, m) : NULL;
	if ((*e)->ExceptionCheck(e)) {
		(*e)->ExceptionClear(e);
		return NULL;
	}
	return r;
}

// display_rotation returns how many quarter turns counterclockwise the
// screen of the activity is turned.
static int display_rotation(uintptr_t env, uintptr_t activity) {
	JNIEnv *e = (JNIEnv *)env;
	jobject wm = call(e, (jobject)activity, "getWindowManager", "()Landroid/view/WindowManager;");
	jobject d = call(e, wm, "getDefaultDisplay", "()Landroid/view/Display;");
	int r = 0;
	if (d) {
		jclass cls = (*e)->GetObjectClass(e, d);
		jmethodID m = (*e)->GetMethodID(e, cls, "getRotation", "()I");
		r = m ? (*e)->CallIntMethod(e, d, m) : 0;
		(*e)->DeleteLocalRef(e, cls);
		(*e)->DeleteLocalRef(e, d);
	}
	if (wm)
		(*e)->DeleteLocalRef(e, wm);
	if ((*e)->ExceptionCheck(e))
		(*e)->ExceptionClear(e);
	return r;
}

// camera_permission reports whether the activity may use the camera, after
// asking the user for it when ask is set. Before Android 6 apps always may.
static int camera_permission(uintptr_t env, uintptr_t activity, int ask) {
	JNIEnv *e = (JNIEnv *)env;
	jobject a = (jobject)activity;
	jclass cls = (*e)->GetObjectClass(e, a);
	jmethodID check = (*e)->GetMethodID(e, cls, "checkSelfPermission", "(Ljava/lang/String;)I");
	jmethodID request = check ? (*e)->GetMethodID(e, cls, "requestPermissions", "([Ljava/lang/String;I)V") : NULL;
	(*e)->DeleteLocalRef(e, cls);
	if (!check || !request) {
		(*e)->ExceptionClear(e);
		return 1;
	}
	jstring name = (*e)->NewStringUTF(e, "android.permission.CAMERA");
	int ok = (*e)->CallIntMethod(e, a, check, name) == 0;
	if (!ok && ask) {
		jclass str = (*e)->FindClass(e, "java/lang/String");
		jobjectArray names = (*e)->NewObjectArray(e, 1, str, name);
		(*e)->CallVoidMethod(e, a, request, names, 1);
		(*e)->DeleteLocalRef(e, names);
		(*e)->DeleteLocalRef(e, str);
	}
	(*e)->DeleteLocalRef(e, name);
	if ((*e)->ExceptionCheck(e)) {
		(*e)->ExceptionClear(e);
		return 0;
	}
	return ok;
}
*/
import "C"

import (
	"errors"
	"fmt"
	"time"
	"unsafe"

	"fyne.io/fyne/v2/driver"
)

func init() {
	displayRotation = func() (turn int) {
		jni(func(env, activity C.uintptr_t) { turn = 90 * int(C.display_rotation(env, activity)) })
		return turn
	}
}

// jni calls f with the JNI environment of the thread and the app's activity.
func jni(f func(env, activity C.uintptr_t)) {
	driver.RunNative(func(ctx any) error {
		if c, ok := ctx.(*driver.AndroidContext); ok {
			f(C.uintptr_t(c.Env), C.uintptr_t(c.Ctx))
		}
		return nil
	})
}

// allowCamera asks the user to let the app use the camera, unless they did,
// and waits up to a minute for the answer.
func allowCamera() error {
	ok := false
	jni(func(env, activity C.uintptr_t) { ok = C.camera_permission(env, activity, 1) != 0 })
	for i := 0; !ok && i < 600; i++ {
		time.Sleep(100 * time.Millisecond)
		jni(func(env, activity C.uintptr_t) { ok = C.camera_permission(env, activity, 0) != 0 })
	}
	if !ok {
		return errors.New("fcam: the user did not allow the camera")
	}
	return nil
}

func devices() ([]Device, error) {
	s := C.ac_devices()
	if s == nil {
		return nil, errUnsupported
	}
	defer C.free(unsafe.Pointer(s))
	return parseDevices(C.GoString(s)), nil
}

func modes(id string) ([]mode, error) {
	var w, h [1024]C.int32_t
	var fps [1024]C.double
	cid := C.CString(id)
	defer C.free(unsafe.Pointer(cid))
	n := C.ac_modes(cid, &w[0], &h[0], &fps[0], C.int(len(w)))
	if n < 0 {
		return nil, fmt.Errorf("fcam: no camera %s", id)
	}
	return modeList(w[:n], h[:n], fps[:n]), nil
}

type stream struct {
	c    *C.acam // in C memory, as the camera's callbacks hold it
	u, v []byte  // chroma deinterleaved
}

func open(id string, m mode) (*stream, error) {
	if err := allowCamera(); err != nil {
		return nil, err
	}
	s := &stream{c: (*C.acam)(C.calloc(1, C.sizeof_acam))}
	cid := C.CString(id)
	defer C.free(unsafe.Pointer(cid))
	if r := C.ac_open(s.c, cid, C.int(m.Width), C.int(m.Height), C.double(m.fps)); r != C.ACAMERA_OK {
		C.free(unsafe.Pointer(s.c))
		return nil, fmt.Errorf("fcam: camera %s failed with error %d", id, r)
	}
	return s, nil
}

func (s *stream) close() {
	C.ac_close(s.c)
	C.free(unsafe.Pointer(s.c))
}

func (s *stream) next(p *picture) (bool, error) {
	switch C.ac_next(s.c) {
	case -1:
		return false, errors.New("fcam: the camera was taken away")
	case 0:
		return false, nil
	}
	c := s.c
	w, h := int(c.width), int(c.height)
	*p = picture{width: w, height: h, y: unsafe.Slice((*byte)(c.y), c.ylen), yStride: int(c.ystride),
		cStride: int(c.cstride), fullRange: true, rotation: int(c.sensor), front: c.front != 0}
	u, v := unsafe.Slice((*byte)(c.u), c.ulen), unsafe.Slice((*byte)(c.v), c.vlen)
	switch {
	case c.cpixel == 1:
		p.u, p.v = u, v
	case c.cpixel == 2 && unsafe.Pointer(c.v) == unsafe.Add(unsafe.Pointer(c.u), 1):
		p.uv = unsafe.Slice((*byte)(c.u), c.ulen+1) // the U plane stops short of the last V
	default: // V first, as NV21, or further apart
		cw, ch, ps, rs := (w+1)/2, (h+1)/2, int(c.cpixel), int(c.cstride)
		s.u, s.v = grow(s.u, cw*ch), grow(s.v, cw*ch)
		for r := range ch {
			for x := range cw {
				s.u[r*cw+x], s.v[r*cw+x] = u[r*rs+x*ps], v[r*rs+x*ps]
			}
		}
		p.u, p.v, p.cStride = s.u, s.v, cw
	}
	return true, nil
}
