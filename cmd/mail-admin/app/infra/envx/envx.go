package envx

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func Get(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func GetInt(key string, fallback int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid int env %s: %w", key, err)
	}
	return n, nil
}

func GetBool(key string, fallback bool) (bool, error) {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if v == "" {
		return fallback, nil
	}
	switch v {
	case "1", "true", "yes", "y", "on":
		return true, nil
	case "0", "false", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid bool env %s: %s", key, v)
	}
}
