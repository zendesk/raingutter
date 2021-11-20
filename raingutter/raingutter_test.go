package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const commandResult = "61"

var RdOut = []struct {
	n string
}{
	{`calling: 1
writing: 2
/tmp/unicorn.sock active: 3
/tmp/unicorn.sock queued: 4`},
	{`calling: 1
writing: 2
127.0.0.1:3000 active: 3
127.0.0.1:3000 queued: 4`},
}

var Lines = []struct {
	n        string
	expected uint64
}{
	{"calling: 1", 1},
	{"127.0.0.1:3000 queued: 4", 4},
	{"/tmp/unicorn.sock active: 3", 3},
}

func TestFetch(t *testing.T) {
	s := status{Ready: true}
	for _, out := range RdOut {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, out.n)
		}))
		defer ts.Close()
		timeout := time.Duration(3 * time.Second)
		httpClient := &http.Client{
			Timeout: timeout,
		}
		res := Fetch(httpClient, ts.URL, &s)
		if res.StatusCode != 200 {
			t.Errorf("raindrops return code is: %v", res.StatusCode)
		}
	}
}

func TestScan(t *testing.T) {
	for _, out := range RdOut {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, out.n)
		}))
		defer ts.Close()
		timeout := time.Duration(3 * time.Second)
		httpClient := &http.Client{
			Timeout: timeout,
		}
		s := status{Ready: true}
		res := Fetch(httpClient, ts.URL, &s)
		r := raingutter{}
		r.Scan(res)
		switch {
		case r.Calling != 1:
			t.Errorf("calling is %v expecting 1", r.Calling)
		case r.Writing != 2:
			t.Errorf("writing is %v expecting 2", r.Writing)
		case r.Active != 3:
			t.Errorf("active is %v expecting 3", r.Active)
		case r.Queued != 4:
			t.Errorf("queued is %v expecting 4", r.Queued)
		}
	}
}

func TestParse(t *testing.T) {
	for _, out := range Lines {
		actual := Parse(out.n)
		if actual != out.expected {
			t.Errorf("Parse(%v): expected %v, actual %v", out.n, out.expected, actual)
		}
	}
}

