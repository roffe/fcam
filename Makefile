.PHONY: linux android install-android windows clean

NDK?=$(HOME)/Android/android-ndk-r30
ADB?=$(HOME)/Android/platform-tools/adb
FYNE?=fyne
WINCC?=x86_64-w64-mingw32-gcc
EXAMPLES=viewer qr track

linux:
	mkdir -p dist
	for e in $(EXAMPLES); do \
		(cd examples/$$e && $(FYNE) build --release -o $(CURDIR)/dist/fcam_$${e}_linux_x64) || exit 1; \
	done

# The examples, debug signed, as --release needs a keystore. fyne package
# leaves the fyneDo migration out of mobile builds, so its tag is given here.
android:
	mkdir -p dist
	for e in $(EXAMPLES); do \
		(cd examples/$$e && ANDROID_NDK_HOME=$(NDK) $(FYNE) package --os android/arm64 \
			--app-id com.roffe.fcam.$$e --name $$e --tags migrated_fynedo) || exit 1; \
		mv examples/$$e/$$e.apk dist/fcam_$${e}_android_arm64.apk; \
	done

install-android: android
	for e in $(EXAMPLES); do $(ADB) install -r -d dist/fcam_$${e}_android_arm64.apk || exit 1; done

windows:
	mkdir -p dist
	for e in $(EXAMPLES); do \
		(cd examples/$$e && CGO_ENABLED=1 CC=$(WINCC) $(FYNE) package --os windows --release \
			--app-id com.roffe.fcam.$$e --name $$e) || exit 1; \
		mv examples/$$e/$$e.exe dist/fcam_$${e}_windows_x64.exe; \
	done

clean:
	rm -rf dist
