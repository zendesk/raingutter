package main

import (
	"fmt"
	"math/rand"
	"net/http"
)

func raindrops(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, `calling: %v
writing: %v
/tmp/unicorn.sock active: %v
/tmp/unicorn.sock queued: %v`, rand.Intn(5),
		rand.Intn(5),
		rand.Intn(10),
		rand.Intn(20))
}

func main() {
	http.HandleFunc("/_raindrops", raindrops)
	for {
		http.ListenAndServe(":3000", nil)
	}
}
