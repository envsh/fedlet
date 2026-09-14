package fbshared

type SendExtra struct {
	RelatesTo []string `json:"relates_to,omitempty"`
	Mentions  []string `json:"mentions,omitempty"`
}
