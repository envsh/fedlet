BINARY = main
TAGS = gomuks,toxoverhttp,emailimap,matrixlite

all:
	go build -v -tags $(TAGS) -o $(BINARY)
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
GCC_DIR = $(NDK_DIR)/toolchains/$(TARGET_TRIPLE)-4.9/prebuilt/$(NDK_HOST)
ANDROID_API ?= 23
TARGET_TRIPLE = aarch64-linux-android
SYSROOT_CFLAGS = --target=$(TARGET_TRIPLE)$(ANDROID_API) \
	--sysroot=$(NDK_DIR)/sysroot \
	-isystem $(NDK_DIR)/sysroot/usr/include/$(TARGET_TRIPLE)
PLATFORM_LIB = $(NDK_DIR)/platforms/android-$(ANDROID_API)/arch-arm64/usr/lib
SYSROOT_LDFLAGS = --target=$(TARGET_TRIPLE)$(ANDROID_API) \
	--sysroot=$(NDK_DIR)/sysroot \
	--gcc-toolchain=$(GCC_DIR) \
	-B $(PLATFORM_LIB) \
	-L $(PLATFORM_LIB) \
	-lc -llog -landroid

a64:
	CGO_ENABLED=1 GOOS=android GOARCH=arm64 \
	CC=$(LLVM_DIR)/bin/clang \
	CGO_CFLAGS="-isystem $(CURDIR)/android/include $(SYSROOT_CFLAGS)" \
	CGO_LDFLAGS="$(SYSROOT_LDFLAGS)" \
	go build -v -tags $(TAGS) -o $(BINARY).arm64
