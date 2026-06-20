BINARY = main
TAGS = gomuks,toxoverhttp,emailimap,matrixlite

# clipboard need CGO
all:
	CGO_ENABLED=1 go build -v -tags $(TAGS) -o $(BINARY)
	@case "$$(uname -s)" in \
		Linux) \
			echo "==> setcap cap_net_admin+eip on $(BINARY)"; \
			sudo setcap cap_net_admin+eip $(BINARY);; \
		Darwin) \
			echo "==> macOS: run with sudo for utun device";; \
	esac

NDK_DIR = /opt/android-ndk
NDK_HOST = $(shell uname -s | tr A-Z a-z)-x86_64
LLVM_DIR = $(NDK_DIR)/toolchains/llvm/prebuilt/$(NDK_HOST)
ANDROID_API ?= 23

AARCH64_TRIPLE = aarch64-linux-android
ARMV7_TRIPLE   = armv7a-linux-androideabi
ARMV7_TRIPLE_C = arm-linux-androideabi

AARCH64_SYSROOT_CFLAGS = --target=$(AARCH64_TRIPLE)$(ANDROID_API) \
	--sysroot=$(NDK_DIR)/sysroot \
	-isystem $(NDK_DIR)/sysroot/usr/include/$(AARCH64_TRIPLE)
ARMV7_SYSROOT_CFLAGS = --target=$(ARMV7_TRIPLE)$(ANDROID_API) \
	--sysroot=$(NDK_DIR)/sysroot \
	-isystem $(NDK_DIR)/sysroot/usr/include/$(ARMV7_TRIPLE_C)

AARCH64_GCC_DIR = $(NDK_DIR)/toolchains/$(AARCH64_TRIPLE)-4.9/prebuilt/$(NDK_HOST)
ARMV7_GCC_DIR   = $(NDK_DIR)/toolchains/$(ARMV7_TRIPLE_C)-4.9/prebuilt/$(NDK_HOST)

AARCH64_PLATFORM_LIB = $(NDK_DIR)/platforms/android-$(ANDROID_API)/arch-arm64/usr/lib
ARMV7_PLATFORM_LIB   = $(NDK_DIR)/platforms/android-$(ANDROID_API)/arch-arm/usr/lib

AARCH64_SYSROOT_LDFLAGS = --target=$(AARCH64_TRIPLE)$(ANDROID_API) \
	--sysroot=$(NDK_DIR)/sysroot \
	--gcc-toolchain=$(AARCH64_GCC_DIR) \
	-B $(AARCH64_PLATFORM_LIB) \
	-L $(AARCH64_PLATFORM_LIB) \
	-lc -llog -landroid
ARMV7_SYSROOT_LDFLAGS = --target=$(ARMV7_TRIPLE)$(ANDROID_API) \
	--sysroot=$(NDK_DIR)/sysroot \
	--gcc-toolchain=$(ARMV7_GCC_DIR) \
	-B $(ARMV7_PLATFORM_LIB) \
	-L $(ARMV7_PLATFORM_LIB) \
	-lc -llog -landroid

a64:
	CGO_ENABLED=1 GOOS=android GOARCH=arm64 \
	CC=$(LLVM_DIR)/bin/clang \
	CGO_CFLAGS="-isystem $(CURDIR)/android/include $(AARCH64_SYSROOT_CFLAGS)" \
	CGO_LDFLAGS="$(AARCH64_SYSROOT_LDFLAGS)" \
	go build -v -tags $(TAGS) -o $(BINARY).arm64

a32:
	CGO_ENABLED=1 GOOS=android GOARCH=arm GOARM=7 \
	CC=$(LLVM_DIR)/bin/clang \
	CGO_CFLAGS="-isystem $(CURDIR)/android/include $(ARMV7_SYSROOT_CFLAGS)" \
	CGO_LDFLAGS="$(ARMV7_SYSROOT_LDFLAGS)" \
	go build -v -tags $(TAGS) -o $(BINARY).arm
