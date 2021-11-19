// Author: Sean Goedecke <sgoedecke@zendesk.com>

package main

import (
	"testing"
)

var SocketLines = []struct {
	raw      string
	expected Socket
}{
	{
		"0: 00000000:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 296045765 1 0000000000000000 100 0 0 10 0",
		Socket{3000, "LISTEN", "296045765", 0},
	},
	{
		"0: 00000000:0BB7 00000000:0000 01 0000000:95 00:00000000 00000000     0        0 123456 1 0000000000000000 100 0 0 10 0",
		Socket{2999, "ESTAB", "123456", 149},
	},
}

func TestParseSocket(t *testing.T) {
	for _, out := range SocketLines {
		actual, err := ParseSocket(out.raw)
		if err != nil {
			t.Errorf("ParseSocket threw error (%v)", err)
		}

		if actual != out.expected {
			t.Errorf("Parse(%v): expected %v, actual %v", out.raw, out.expected, actual)
		}
	}
}

var SocketErrorLines = []string{
	"",
	"foo bar",
	"0: 0000000010BB8 0000000010000 0A 00000000:00000000 00100000000 00000000     0        0 296045765 1 0000000000000000 100 0 0 10 0",
}

func TestParseSocketErrors(t *testing.T) {
	for _, line := range SocketErrorLines {
		_, err := ParseSocket(line)
		if err == nil {
			t.Errorf("ParseSocket did not raise error for (%v)", line)
		}
	}
}

var SocketStatsLines = []struct {
	raw      string
	port     uint16
	expected SocketStats
}{
	{
		"0: 00000000:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 296045765 1 0000000000000000 100 0 0 10 0",
		3000,
		SocketStats{0, 0, 296045765},
	},
	{
		"0: 00000000:0BB8 00000000:0000 0A 00000000:29A 00:00000000 00000000     0        0 296045765 1 0000000000000000 100 0 0 10 0",
		3000,
		SocketStats{666, 0, 296045765},
	},
	{
		"0: 00000000:0BB7 00000000:0000 0A 00000000:8999 00:00000000 00000000     0        0 296045765 1 0000000000000000 100 0 0 10 0",
		3000,
		SocketStats{0, 0, 0},
	},
	{
		`0: 00000000:0BB8 00000000:0000 0A 00000000:8999 00:00000000 00000000     0        0 296045765 1 0000000000000000 100 0 0 10 0
        1: 00000000:0BB8 00000000:0000 01 00000000:8999 00:00000000 00000000     0        0 296045765 1 0000000000000000 100 0 0 10 0`,
		3000,
		SocketStats{35225, 1, 296045765},
	},
}

func TestParseSocketStats(t *testing.T) {
	for _, out := range SocketStatsLines {
		actual, err := ParseSocketStats(out.port, out.raw)
		if err != nil {
			t.Errorf("ParseSocketStats threw error (%v)", err)
		}

		if *actual != out.expected {
			t.Errorf("Parse(%v): expected %v, actual %v", out.raw, out.expected, actual)
		}
	}
}
