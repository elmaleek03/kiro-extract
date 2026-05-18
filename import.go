package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRouterURL    = "http://localhost:20128"
	defaultTokensFile   = "kiro-out/kiro-refresh-tokens.txt"
	defaultDelaySeconds = 1
	importPath          = "/api/oauth/kiro/import"
)

type importSuccess struct {
	Success    bool `json:"success"`
	Connection struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Email    any    `json:"email"`
	} `json:"connection"`
}

type importError struct {
	Error string `json:"error"`
}

func runImport(stdin *bufio.Reader) error {
	fmt.Println()
	fmt.Println("--- import to 9router ---")

	env, _ := loadEnv(".env")

	baseURL := env["BASE_URL"]
	if baseURL == "" {
		baseURL = defaultRouterURL
	}
	baseURL = strings.TrimRight(prompt(stdin, fmt.Sprintf("base URL [%s]> ", baseURL), baseURL), "/")

	authToken := env["AUTH_TOKEN"]
	if authToken == "" {
		authToken = strings.TrimSpace(prompt(stdin, "auth_token cookie value> ", ""))
	} else {
		fmt.Printf("auth_token loaded from .env (len=%d)\n", len(authToken))
		if strings.EqualFold(prompt(stdin, "override? [y/N]> ", "n"), "y") {
			authToken = strings.TrimSpace(prompt(stdin, "auth_token> ", ""))
		}
	}
	if authToken == "" {
		return fmt.Errorf("auth_token cookie is required")
	}

	tokensFile := env["TOKENS_FILE"]
	if tokensFile == "" {
		tokensFile = defaultTokensFile
	}
	tokensFile = prompt(stdin, fmt.Sprintf("tokens file [%s]> ", tokensFile), tokensFile)

	delaySec := defaultDelaySeconds
	if v := env["DELAY_SECONDS"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			delaySec = n
		}
	}
	delayInput := prompt(stdin, fmt.Sprintf("delay seconds [%d]> ", delaySec), fmt.Sprintf("%d", delaySec))
	if n, err := strconv.Atoi(delayInput); err == nil && n >= 0 {
		delaySec = n
	}

	insecure := strings.EqualFold(env["INSECURE"], "true")
	if strings.HasPrefix(strings.ToLower(baseURL), "https://") {
		insecure = strings.EqualFold(prompt(stdin, fmt.Sprintf("skip TLS verify? [%s]> ", boolStr(insecure)), boolStr(insecure)), "true") ||
			strings.EqualFold(prompt(stdin, "", boolStr(insecure)), "y")
	}

	tokens, err := readTokenList(tokensFile)
	if err != nil {
		return fmt.Errorf("read tokens: %w", err)
	}
	if len(tokens) == 0 {
		return fmt.Errorf("no tokens found in %s", tokensFile)
	}

	fmt.Printf("\nready to import %d tokens to %s%s (delay=%ds)\n", len(tokens), baseURL, importPath, delaySec)
	if !strings.EqualFold(prompt(stdin, "proceed? [Y/n]> ", "y"), "y") {
		fmt.Println("aborted")
		return nil
	}

	tr := &http.Transport{}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	client := &http.Client{Timeout: httpTimeout, Transport: tr}
	endpoint := baseURL + importPath

	var ok, fail int
	fmt.Println()
	for i, tok := range tokens {
		preview := tok
		if len(preview) > 24 {
			preview = preview[:24] + "..."
		}
		fmt.Printf("[%d/%d] %s -> ", i+1, len(tokens), preview)

		status, body, err := postImport(client, endpoint, authToken, tok)
		if err != nil {
			fail++
			fmt.Printf("ERROR %v\n", err)
		} else {
			line := summarizeImport(status, body)
			if status >= 200 && status < 300 {
				ok++
			} else {
				fail++
			}
			fmt.Println(line)
		}

		if i < len(tokens)-1 && delaySec > 0 {
			time.Sleep(time.Duration(delaySec) * time.Second)
		}
	}

	fmt.Printf("\ndone: %d ok, %d failed (total %d)\n", ok, fail, len(tokens))
	return nil
}

func postImport(client *http.Client, endpoint, authToken, refreshToken string) (int, []byte, error) {
	payload, err := json.Marshal(map[string]string{"refreshToken": refreshToken})
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "kiro-import/1.0")
	req.AddCookie(&http.Cookie{Name: "auth_token", Value: authToken})

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func summarizeImport(status int, body []byte) string {
	trimmed := bytes.TrimSpace(body)

	var ok importSuccess
	if err := json.Unmarshal(trimmed, &ok); err == nil && ok.Success {
		email := "<no email>"
		if s, isStr := ok.Connection.Email.(string); isStr && s != "" {
			email = s
		}
		return fmt.Sprintf("OK (%d) id=%s email=%s", status, ok.Connection.ID, email)
	}
	var er importError
	if err := json.Unmarshal(trimmed, &er); err == nil && er.Error != "" {
		return fmt.Sprintf("FAIL (%d) %s", status, er.Error)
	}
	s := string(trimmed)
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return fmt.Sprintf("FAIL (%d) %s", status, s)
}

func readTokenList(path string) ([]string, error) {
	expanded, err := expandHome(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(expanded)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

func loadEnv(path string) (map[string]string, error) {
	out := map[string]string{}
	expanded, err := expandHome(path)
	if err != nil {
		return out, err
	}
	f, err := os.Open(expanded)
	if err != nil {
		if os.IsNotExist(err) {
			merge(out, fromOSEnv())
			return out, nil
		}
		return out, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		v = strings.Trim(v, `"'`)
		out[k] = v
	}
	merge(out, fromOSEnv())
	return out, sc.Err()
}

func fromOSEnv() map[string]string {
	keys := []string{"BASE_URL", "AUTH_TOKEN", "TOKENS_FILE", "DELAY_SECONDS", "INSECURE"}
	m := map[string]string{}
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok && v != "" {
			m[k] = v
		}
	}
	return m
}

func merge(dst, src map[string]string) {
	for k, v := range src {
		dst[k] = v
	}
}
