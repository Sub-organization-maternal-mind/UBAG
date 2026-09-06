package main

import (
	"fmt"
	"strings"
)

func hostnameOf(url string) string {
	s := url
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
	}
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[:idx]
	}
	if idx := strings.LastIndex(s, "@"); idx >= 0 {
		s = s[idx+1:]
	}
	if idx := strings.LastIndex(s, ":"); idx >= 0 {
		s = s[:idx]
	}
	return strings.ToLower(s)
}

func main() {
	cases := []string{
		"https://[::1]:8080/path",  // IPv6 literal
		"https://host.com:443",    // port, no path
		"example.com/path",        // no scheme
		"https://host.com",        // no path, no port
	}
	for _, u := range cases {
		fmt.Printf("%-40s => %q\n", u, hostnameOf(u))
	}
}
