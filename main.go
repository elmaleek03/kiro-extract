// Command kiro is an interactive CLI that helps you:
//
//  1. Extract Kiro provider credentials from an enowxai proxy daemon.
//  2. Import refresh tokens into a 9router dashboard via /api/oauth/kiro/import.
//
// Run the binary (or `go run .`) and follow the menu.
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func main() {
	stdin := bufio.NewReader(os.Stdin)
	for {
		fmt.Println()
		fmt.Println("=========================================")
		fmt.Println(" kiro-extract: enowxai <-> 9router tools")
		fmt.Println("=========================================")
		fmt.Println(" 1) Extract Kiro credentials from enowxai")
		fmt.Println(" 2) Import refresh tokens into 9router")
		fmt.Println(" q) Quit")
		fmt.Println()
		choice := strings.ToLower(prompt(stdin, "select> ", ""))

		switch choice {
		case "1":
			if err := runExtract(stdin); err != nil {
				fmt.Fprintln(os.Stderr, "extract failed:", err)
			}
		case "2":
			if err := runImport(stdin); err != nil {
				fmt.Fprintln(os.Stderr, "import failed:", err)
			}
		case "q", "quit", "exit", "":
			fmt.Println("bye")
			return
		default:
			fmt.Println("unknown choice:", choice)
		}
	}
}
