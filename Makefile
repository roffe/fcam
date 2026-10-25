.PHONY: linux android install-android windows clean

NDK?=$(HOME)/Android/android-ndk-r30
ADB?=$(HOME)/Android/platform-tools/adb
FYNE?=fyne
WINCC?=x86_64-w64-mingw32-gcc

linux:
	mkdir -p dist
	cd example && $(FYNE) build --release -o $(CURDIR)/dist/fcam_linux_x64

# The example demo, debug signed, as --release needs a keystore. fyne package
# leaves the fyneDo migration out of mobile builds, so its tag is given here.
android:
	mkdir -p dist
	cd example && ANDROID_NDK_HOME=$(NDK) $(FYNE) package --os android/arm64 \
		--app-id com.roffe.fcam --name fcam --tags migrated_fynedo
	mv example/fcam.apk dist/fcam_android_arm64.apk

install-android: android
	$(ADB) install -r -d dist/fcam_android_arm64.apk

windows:
	mkdir -p dist
	cd example && CGO_ENABLED=1 CC=$(WINCC) $(FYNE) package --os windows --release \
		--app-id com.roffe.fcam --name fcam
	mv example/fcam.exe dist/fcam_windows_x64.exe

clean:
	rm -rf dist
