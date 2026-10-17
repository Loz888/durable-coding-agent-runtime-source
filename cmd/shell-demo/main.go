package main

import (
	"fmt"
	"os/exec"
)

func main() {
	cmd := exec.Command(
		"echo hello",
	)

	output, err := cmd.Output()

	fmt.Println("output:", string(output))
	fmt.Println("error:", err)
}
