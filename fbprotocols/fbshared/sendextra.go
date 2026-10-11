package fbshared

type SendExtra struct {
	RelatesTo []string `json:"relates_to,omitempty"`
	Mentions  []string `json:"mentions,omitempty"`
	// Visibility 显式消息可见性(如 misskey 的 public/home/followers/specified),
	// 经 /api/messages/send 的 visibility 表单字段下发,各协议按需消费。
	Visibility string `json:"visibility,omitempty"`
}
