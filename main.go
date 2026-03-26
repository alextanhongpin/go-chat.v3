package main

import (
	"flag"
	"log"
	"log/slog"
	"net/http"

	"github.com/redis/go-redis/v9"
)

var addr = flag.String("addr", ":8080", "http service address")

func serveHome(w http.ResponseWriter, r *http.Request) {
	log.Println(r.URL)
	if r.URL.Path != "/" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.ServeFile(w, r, "home.html")
}

func main() {
	flag.Parse()

	rdb := redis.NewClient(&redis.Options{
		Addr:     "localhost:6379", // use default address
		Password: "",               // no password set
		DB:       0,                // use default DB
	})

	http.HandleFunc("/", serveHome)
	http.Handle("/ws", &wsServer{rdb})
	slog.Info("listening to server. press ctrl+c to cancel", "port", *addr)
	err := http.ListenAndServe(*addr, nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
