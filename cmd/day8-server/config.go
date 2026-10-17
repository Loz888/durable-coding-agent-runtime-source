package main

import (
	"bufio"
	"errors"
	"os"
	"strings"
)

// The exported environment takes precedence over the local project .env file.
func databaseURL() (string, error) {
	if value := strings.TrimSpace(os.Getenv("DATABASE_URL")); value != "" {
		return value, nil
	}
	file, err := os.Open(".env")
	if err != nil {
		return "", errors.New("set DATABASE_URL or add DATABASE_URL to the project .env file")
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "DATABASE_URL" {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		if value != "" {
			return value, nil
		}
	}
	if scanner.Err() != nil {
		return "", errors.New("cannot read project .env file")
	}
	return "", errors.New("DATABASE_URL is empty in the environment and project .env file")
}
