package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
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

func fakeExecCommand(command string, args ...string) *exec.Cmd {
	cs := []string{"-test.run=TestHelperProcess", "--", command}
	cs = append(cs, args...)
	cmd := exec.Command(os.Args[0], cs...)
	cmd.Env = []string{"GO_WANT_HELPER_PROCESS=1"}
	return cmd
}

func TestUnicornWorkersPgrep(t *testing.T) {
	execCommand = fakeExecCommand
	defer func() { execCommand = exec.Command }()
	tc := totalConnections{Count: 0}
	getWorkers(&tc)
	result, err := strconv.ParseUint(commandResult, 10, 64)
	if err != nil {
		t.Errorf("%v", err)
	}
	if tc.Count != result-1 {
		t.Errorf("Unicorn workers is: %v. It should be %v", tc.Count, result-1)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	// mocking "pgrep -fc helper.sh"
	fmt.Fprintf(os.Stdout, commandResult)
	os.Exit(0)
}

func TestUnicornWorkerPgrepError(t *testing.T) {
	tc := totalConnections{Count: 0}
	getWorkers(&tc)
	if tc.Count != 0 {
		t.Errorf("Unicorn workers is: %v. It should be 0", tc.Count)
	}

}

func TestUnicornWorkerEnv(t *testing.T) {
	os.Setenv("UNICORN_WORKERS", "16")
	tc := totalConnections{Count: 0}
	getWorkers(&tc)
	if tc.Count != 16 {
		t.Errorf("Unicorn workers is: %v. It should be 16", tc.Count)
	}
}
