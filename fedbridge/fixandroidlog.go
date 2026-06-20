package main

import (
	"log"
	"os"
	"runtime"
	"syscall"
)

func init() {
	if runtime.GOOS != "android" {
		return
	}
	syscall.Dup2(0, 1)
	syscall.Dup2(0, 2)
	log.SetOutput(os.Stderr)
}
