// anet 在 android+cgo 下 #include <android/api-level.h> 并调用
// android_get_device_api_level()。该头文件自 NDK r23 才引入，
// NDK r18 不存在。
//
// 依赖链: main.go → p2put → go-libp2p → quic-go → anet

#ifndef FEDLET_ANDROID_API_STUB_H
#define FEDLET_ANDROID_API_STUB_H

static inline int android_get_device_api_level(void) {
    return -1;
}

#endif
