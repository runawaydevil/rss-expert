package config

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"strings"
)

var envFiles = []string{".env.local", ".env"}

const byteOrderMark = "\xef\xbb\xbf"

func LoadEnvFiles() error {
	if named := os.Getenv(envPrefix + "ENV_FILE"); named != "" {
		return readEnvFile(named, true)
	}
	for _, name := range envFiles {
		if err := readEnvFile(name, false); err != nil {
			return err
		}
	}
	return nil
}

func readEnvFile(path string, required bool) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) && !required {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	lines := bufio.NewScanner(f)
	for lines.Scan() {
		key, value, ok := parseEnvLine(lines.Text())
		if !ok {
			continue
		}
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return lines.Err()
}

func parseEnvLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(strings.TrimPrefix(line, byteOrderMark))
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")

	key, value, found := strings.Cut(line, "=")
	key = strings.TrimSpace(key)
	if !found || key == "" {
		return "", "", false
	}

	value = strings.TrimSpace(value)
	switch {
	case len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"',
		len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'':
		value = value[1 : len(value)-1]
	default:
		if hash := strings.Index(value, " #"); hash >= 0 {
			value = strings.TrimSpace(value[:hash])
		}
	}
	return key, value, true
}
