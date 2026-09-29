package main

import "testing"

// The password crosses the network in the clear only over http to another host.
func TestPlainHTTPToAnotherHostIsWarnedAbout(t *testing.T) {
	for server, warned := range map[string]bool{
		"http://127.0.0.1:8000":    false,
		"http://localhost:8000":    false,
		"http://[::1]:8000":        false,
		"https://chat.example":     false,
		"http://chat.example:8000": true,
		"http://192.168.1.20:8000": true,
	} {
		if got := cleartext(server); got != warned {
			t.Errorf("cleartext(%q) = %v", server, got)
		}
	}
}
