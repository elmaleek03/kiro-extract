package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	defaultAPIBase          = "https://enowxai-dashboard.elmaleek.me"
	defaultAccountsPath     = "/api/accounts"
	defaultSessionCookieName = "enowxai_session"
	defaultOutDir           = "kiro-out"
	httpTimeout             = 30 * time.Second
)

type authFile struct {
	LicenseKey string `json:"license_key"`
	Token      string `json:"token"`
}

type credentials struct {
	AccessToken  string    `json:"access_token,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresAt    string    `json:"expires_at,omitempty"`
	ExpiresIn    flexInt64 `json:"expires_in,omitempty"`
	ProfileARN   string    `json:"profile_arn,omitempty"`
	PlanType     string    `json:"plan_type,omitempty"`
}

// flexInt64 decodes a JSON value that may be a number or a numeric string.
type flexInt64 int64

func (f *flexInt64) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "" || s == "null" {
		return nil
	}
	s = strings.Trim(s, `"`)
	if s == "" {
		return nil
	}
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return fmt.Errorf("flexInt64: %w", err)
	}
	*f = flexInt64(n)
	return nil
}

type account struct {
	ID               string      `json:"id"`
	Email            string      `json:"email"`
	Provider         string      `json:"provider"`
	Status           string      `json:"status"`
	RemainingCredits float64     `json:"remaining_credits"`
	LastRefreshAt    *string     `json:"last_refresh_at"`
	Credentials      credentials `json:"credentials"`
}

type credsResp struct {
	Data []json.RawMessage `json:"data"`
}

type slimAccount struct {
	Email            string  `json:"email"`
	ID               string  `json:"id"`
	Status           string  `json:"status"`
	RemainingCredits float64 `json:"remaining_credits"`
	LastRefreshAt    string  `json:"last_refresh_at,omitempty"`
	AccessToken      string  `json:"access_token"`
	RefreshToken     string  `json:"refresh_token"`
	ExpiresAt        string  `json:"expires_at,omitempty"`
	ExpiresIn        int64   `json:"expires_in,omitempty"`
	ProfileARN       string  `json:"profile_arn,omitempty"`
	PlanType         string  `json:"plan_type,omitempty"`
}

func runExtract(stdin *bufio.Reader) error {
	fmt.Println()
	fmt.Println("--- extract ---")
	fmt.Println(" 1) paste enowxai_session cookie value (default)")
	fmt.Println(" 2) read session cookie from .env (ENOWXAI_SESSION)")
	fmt.Println(" 3) read session cookie from a host auth.json file")
	fmt.Println(" b) back")
	src := strings.ToLower(prompt(stdin, "source> ", "1"))

	var sessionCookie string
	switch src {
	case "1", "":
		sessionCookie = strings.TrimSpace(prompt(stdin, "enowxai_session cookie> ", ""))
		if sessionCookie == "" {
			return errors.New("empty session cookie")
		}
	case "2":
		env, _ := loadEnv(".env")
		sessionCookie = env["ENOWXAI_SESSION"]
		if sessionCookie == "" {
			return errors.New("ENOWXAI_SESSION not found in .env")
		}
		fmt.Printf("loaded session cookie from .env (len=%d)\n", len(sessionCookie))
	case "3":
		def := defaultHostAuthPath()
		path := prompt(stdin, fmt.Sprintf("path to auth.json [%s]> ", def), def)
		t, err := tokenFromHost(path)
		if err != nil {
			return err
		}
		sessionCookie = t
	case "b", "back":
		return nil
	default:
		return fmt.Errorf("unknown source %q", src)
	}

	apiBase := prompt(stdin, fmt.Sprintf("api base [%s]> ", defaultAPIBase), defaultAPIBase)
	outDir := prompt(stdin, fmt.Sprintf("output directory [%s]> ", defaultOutDir), defaultOutDir)

	fmt.Printf("\nfetching accounts from %s ...\n", apiBase)
	raw, err := fetchAccounts(apiBase, sessionCookie, httpTimeout)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	rawPath := filepath.Join(outDir, "all-credentials.json")
	if err := os.WriteFile(rawPath, raw, 0o600); err != nil {
		return err
	}
	fmt.Printf("wrote %s (%d bytes)\n", rawPath, len(raw))

	kiroFull, kiroSlim, err := splitKiro(raw)
	if err != nil {
		return err
	}

	files := map[string]func() error{
		"kiro-full.json":          func() error { return writeJSON(filepath.Join(outDir, "kiro-full.json"), kiroFull) },
		"kiro-credentials.json":   func() error { return writeJSON(filepath.Join(outDir, "kiro-credentials.json"), kiroSlim) },
		"kiro-credentials.csv":    func() error { return writeCSV(filepath.Join(outDir, "kiro-credentials.csv"), kiroSlim) },
		"kiro-token-refresh.txt":  func() error { return writeTokenRefresh(filepath.Join(outDir, "kiro-token-refresh.txt"), kiroSlim) },
		"kiro-refresh-tokens.txt": func() error { return writeRefreshTokens(filepath.Join(outDir, "kiro-refresh-tokens.txt"), kiroSlim) },
	}
	for name, fn := range files {
		if err := fn(); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}

	fmt.Printf("done: %d kiro accounts -> %s\n", len(kiroSlim), outDir)
	return nil
}

func tokenFromHost(path string) (string, error) {
	expanded, err := expandHome(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(expanded)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", expanded, err)
	}
	return parseAuthToken(data)
}

func parseAuthToken(data []byte) (string, error) {
	var a authFile
	if err := json.Unmarshal(data, &a); err != nil {
		return "", fmt.Errorf("parse auth.json: %w", err)
	}
	if strings.TrimSpace(a.Token) == "" {
		return "", errors.New("auth.json has no token field")
	}
	return a.Token, nil
}

func defaultHostAuthPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, ".enowxai", "auth.json")
	default:
		return filepath.Join(home, ".enowxai", "auth.json")
	}
}

func fetchAccounts(apiBase, sessionCookie string, timeout time.Duration) ([]byte, error) {
	url := strings.TrimRight(apiBase, "/") + defaultAccountsPath
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", defaultSessionCookieName+"="+sessionCookie)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/149.0.0.0 Safari/537.36")
	req.Header.Set("Referer", strings.TrimRight(apiBase, "/")+"/accounts/standard")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("upstream HTTP %d: %s", resp.StatusCode, truncate(string(body), 300))
	}
	return body, nil
}

func splitKiro(raw []byte) (full []json.RawMessage, slim []slimAccount, err error) {
	// Try {data: [...]} wrapper first (enowxai proxy format).
	var resp credsResp
	if err = json.Unmarshal(raw, &resp); err == nil && len(resp.Data) > 0 {
		return splitKiroFromRaw(resp.Data)
	}
	// Try direct array (dashboard format).
	var arr []json.RawMessage
	if err = json.Unmarshal(raw, &arr); err == nil && len(arr) > 0 {
		return splitKiroFromRaw(arr)
	}
	return nil, nil, fmt.Errorf("decode response: unrecognized JSON structure (first 200 bytes: %s)", truncate(string(raw), 200))
}

func splitKiroFromRaw(items []json.RawMessage) (full []json.RawMessage, slim []slimAccount, err error) {
	var skipped int
	for _, item := range items {
		var a account
		if err := json.Unmarshal(item, &a); err != nil {
			skipped++
			continue
		}
		if a.Provider != "kiro" {
			continue
		}
		full = append(full, item)
		slim = append(slim, slimAccount{
			Email:            a.Email,
			ID:               a.ID,
			Status:           a.Status,
			RemainingCredits: a.RemainingCredits,
			LastRefreshAt:    strVal(a.LastRefreshAt),
			AccessToken:      a.Credentials.AccessToken,
			RefreshToken:     a.Credentials.RefreshToken,
			ExpiresAt:        a.Credentials.ExpiresAt,
			ExpiresIn:        int64(a.Credentials.ExpiresIn),
			ProfileARN:       a.Credentials.ProfileARN,
			PlanType:         a.Credentials.PlanType,
		})
	}
	if skipped > 0 {
		fmt.Fprintf(os.Stderr, "warning: skipped %d undecodable account records\n", skipped)
	}
	return full, slim, nil
}

func writeJSON(path string, v any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeCSV(path string, accounts []slimAccount) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{
		"email", "refresh_token", "access_token", "expires_at", "expires_in",
		"profile_arn", "remaining_credits", "status", "plan_type",
	}); err != nil {
		return err
	}
	for _, a := range accounts {
		expIn := ""
		if a.ExpiresIn != 0 {
			expIn = fmt.Sprintf("%d", a.ExpiresIn)
		}
		if err := w.Write([]string{
			a.Email,
			a.RefreshToken,
			a.AccessToken,
			a.ExpiresAt,
			expIn,
			a.ProfileARN,
			fmt.Sprintf("%g", a.RemainingCredits),
			a.Status,
			a.PlanType,
		}); err != nil {
			return err
		}
	}
	return nil
}

func writeTokenRefresh(path string, accounts []slimAccount) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, a := range accounts {
		if _, err := fmt.Fprintf(f, "%s:%s\n", a.AccessToken, a.RefreshToken); err != nil {
			return err
		}
	}
	return nil
}

func writeRefreshTokens(path string, accounts []slimAccount) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, a := range accounts {
		if a.RefreshToken == "" {
			continue
		}
		if _, err := fmt.Fprintln(f, a.RefreshToken); err != nil {
			return err
		}
	}
	return nil
}
