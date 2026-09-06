package bdtieba

type FrsResp struct {
	ErrorCode int     `json:"error_code"`
	ErrorMsg  string  `json:"error_msg"`
	Data      FrsData `json:"data"`
}

type FrsData struct {
	Forum      Forum    `json:"forum"`
	ThreadList []Thread `json:"thread_list"`
	Page       Page     `json:"page"`
}

type Forum struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type Thread struct {
	Tid         int64             `json:"tid"`
	Title       string            `json:"title,omitempty"`
	Abstract    []AbstractContent `json:"abstract,omitempty"`
	ReplyNum    int64             `json:"reply_num"`
	CreateTime  int64             `json:"create_time"`
	LastTimeInt int64             `json:"last_time_int"`
	IsTop       int               `json:"is_top"`
	Author      *Author           `json:"author,omitempty"`
}

type AbstractContent struct {
	Text string `json:"text,omitempty"`
}

type Author struct {
	ID           int64  `json:"id"`
	Name         string `json:"name,omitempty"`
	NameShow     string `json:"name_show,omitempty"`
	Portrait     string `json:"portrait,omitempty"`
	ShowNickname string `json:"show_nickname,omitempty"`
}

type Page struct {
	CurrentPage int `json:"current_page"`
	HasMore     int `json:"has_more"`
	TotalNum    int `json:"total_num"`
	TotalPage   int `json:"total_page"`
}