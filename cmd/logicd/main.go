// logicd 是门电路事件驱动回放服务。
package main

import (
	"log"
	"net/http"
	"os"

	"gatesim/internal/logic"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	log.Printf("logic service listening on %s", addr)
	if err := http.ListenAndServe(addr, logic.Handler()); err != nil {
		log.Fatal(err)
	}
}
