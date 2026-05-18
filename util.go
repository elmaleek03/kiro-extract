package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// prompt reads a single line from stdin, trims whitespace, and returns the
// default if the input was empty.
func prompt(r *bufio.Reader, label, def string) string {
	if label != "" {
		fmt.Print(label)
	}
	line, err := r.ReadString('\n')
	if err != nil {
		fmt.Println()
		return def
	}
	line = strings.TrimRight(line, "\r\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func expandHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if p == "~" {
		return home, nil
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:]), nil
	}
	return p, nil
}
