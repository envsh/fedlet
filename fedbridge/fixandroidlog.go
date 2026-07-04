//go:build android

package main

import (
	"log"
	"os"
	"syscall"
)

// 在 Android 上，Dup3 比 Dup2 更通用——所有 Android 架构都支持 Dup3（内核 ≥ 2.6.27）。
func init() {
	syscall.Dup3(0, 1, 0)
	syscall.Dup3(0, 2, 0)
	log.SetOutput(os.Stderr)
}
