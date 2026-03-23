package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultSourceDir  = "/source"
	defaultRetryCount = 3
	defaultRetryDelay = 2 * time.Second
)

type Settings struct {
	BaseURL             string
	Email               string
	Password            string
	DefaultStorage      string
	SourceDir           string
	Retries             int
	RetryDelay          time.Duration
	TelegramBotToken    string
	TelegramChatID      string
}

func Load(envFile string) (Settings, error) {
	fileValues := map[string]string{}
	if envFile != "" {
		values, err := parseDotEnvFile(envFile)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Settings{}, err
		}
		fileValues = values
	}

	cfg := Settings{
		BaseURL:          lookupValue("PENTARACT_BASE_URL", fileValues),
		Email:            lookupValue("PENTARACT_EMAIL", fileValues),
		Password:         lookupValue("PENTARACT_PASSWORD", fileValues),
		DefaultStorage:   lookupValue("PENTARACT_STORAGE", fileValues),
		SourceDir:        defaultString(lookupValue("PENTARACT_SOURCE_DIR", fileValues), defaultSourceDir),
		Retries:          defaultRetryCount,
		RetryDelay:       defaultRetryDelay,
		TelegramBotToken: lookupValue("TELEGRAM_BOT_TOKEN", fileValues),
		TelegramChatID:   lookupValue("TELEGRAM_CHAT_ID", fileValues),
	}

	if raw := lookupValue("PENTARACT_RETRIES", fileValues); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return Settings{}, fmt.Errorf("invalid PENTARACT_RETRIES value %q", raw)
		}
		cfg.Retries = n
	}

	if raw := lookupValue("PENTARACT_RETRY_DELAY", fileValues); raw != "" {
		delay, err := time.ParseDuration(raw)
		if err != nil || delay < 0 {
			return Settings{}, fmt.Errorf("invalid PENTARACT_RETRY_DELAY value %q", raw)
		}
		cfg.RetryDelay = delay
	}

	return cfg, nil
}

func parseDotEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return parseDotEnv(f)
}

func parseDotEnv(r io.Reader) (map[string]string, error) {
	values := map[string]string{}
	scanner := bufio.NewScanner(r)
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("invalid .env line %d", lineNo)
		}

		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			return nil, fmt.Errorf("invalid .env line %d: empty key", lineNo)
		}

		parsed, err := parseEnvValue(value)
		if err != nil {
			return nil, fmt.Errorf("invalid .env line %d: %w", lineNo, err)
		}
		values[key] = parsed
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return values, nil
}

func parseEnvValue(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	switch raw[0] {
	case '"':
		return parseDoubleQuotedValue(raw)
	case '\'':
		if len(raw) < 2 || raw[len(raw)-1] != '\'' {
			return "", errors.New("unterminated single-quoted value")
		}
		return raw[1 : len(raw)-1], nil
	default:
		if idx := strings.Index(raw, " #"); idx >= 0 {
			raw = raw[:idx]
		}
		return strings.TrimSpace(raw), nil
	}
}

func parseDoubleQuotedValue(raw string) (string, error) {
	if len(raw) < 2 || raw[len(raw)-1] != '"' {
		return "", errors.New("unterminated double-quoted value")
	}

	content := raw[1 : len(raw)-1]
	content = strings.ReplaceAll(content, `\n`, "\n")
	content = strings.ReplaceAll(content, `\r`, "\r")
	content = strings.ReplaceAll(content, `\t`, "\t")
	content = strings.ReplaceAll(content, `\"`, `"`)
	content = strings.ReplaceAll(content, `\\`, `\`)
	return content, nil
}

func lookupValue(key string, fileValues map[string]string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fileValues[key]
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
