package fbshared

type MediaDataInfo struct {
	MimeType string
	Size     int
	Width    int
	Height   int
	Filename string
	MsgType  string
}

type SendResult struct {
	MsgID string
}
