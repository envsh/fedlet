package main

import (
	"time"
	"log"
	"fmt"
)

func poll_demopub() {
	// var channel_name = "v2ex"

	for i := 0; ; i++ {
		time.Sleep(15*time.Second)
		scc := fmt.Sprintf("{\"vvv\": ddddddd %v}", i)
		err := publish(channel_name, []byte(scc))
		if err != nil {
			log.Println(i, err)
		}
	}
}
