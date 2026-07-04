package main

import (
	"bytes"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"strings"

	"github.com/envsh/fedlet/fbprotocols/fbshared"
)

func getMediaDataInfo(data []byte, filename string) fbshared.MediaDataInfo {
	info := fbshared.MediaDataInfo{
		Size:     len(data),
		Filename: filename,
	}
	info.MimeType = http.DetectContentType(data)

	switch {
	case strings.HasPrefix(info.MimeType, "image/"):
		info.MsgType = "image"
		cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
		if err == nil {
			info.Width = cfg.Width
			info.Height = cfg.Height
		}
	case strings.HasPrefix(info.MimeType, "video/"):
		info.MsgType = "video"
	case strings.HasPrefix(info.MimeType, "audio/"):
		info.MsgType = "audio"
	default:
		info.MsgType = "file"
	}
	return info
}
