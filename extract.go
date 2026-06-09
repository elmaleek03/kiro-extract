package main

import (
	"bufio"
	"bytes"
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
	defaultLocalAPIBase     = "http://localhost:1431"
	defaultExternalAPIBase  = "https://enowxai-dashboard.elmaleek.me"
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

type localAccount struct {
	ID               string  `json:"id"`
	Email            string  `json:"email"`
	Provider         string  `json:"provider"`
	Status           string  `json:"status"`
	RemainingCredits float64 `json:"remaining_credits"`
	PlanType         string  `json:"plan_type"`
	PlanDisplay      string  `json:"plan_display"`
}

type localAccountsResp struct {
	Accounts []localAccount `json:"accounts"`
}

type desktopTokenResp struct {
	RefreshToken string `json:"refresh_token"`
	AuthJSON     string `json:"auth_json"`
	Found        bool   `json:"found"`
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
	fmt.Println(" 1) local dashboard (password login, localhost:1431)")
	fmt.Println(" 2) external dashboard (session cookie)")
	fmt.Println(" 3) read session cookie from .env (ENOWXAI_SESSION)")
	fmt.Println(" b) back")
	src := strings.ToLower(prompt(stdin, "source> ", "1"))

	switch src {
	case "1", "":
		return runExtractLocal(stdin)
	case "2":
		return runExtractExternal(stdin)
	case "3":
		return runExtractFromEnv(stdin)
	case "b", "back":
		return nil
	default:
		return fmt.Errorf("unknown source %q", src)
	}
}

func runExtractLocal(stdin *bufio.Reader) error {
	apiBase := prompt(stdin, fmt.Sprintf("api base [%s]> ", defaultLocalAPIBase), defaultLocalAPIBase)
	password := prompt(stdin, "dashboard password> ", "")
	if password == "" {
		return errors.New("empty dashboard password")
	}
	outDir := prompt(stdin, fmt.Sprintf("output directory [%s]> ", defaultOutDir), defaultOutDir)

	client := &http.Client{Timeout: httpTimeout}

	fmt.Printf("\nlogging in to %s ...\n", apiBase)
	sessionCookie, err := loginDashboard(client, apiBase, password)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	fmt.Println("login ok")

	fmt.Println("fetching accounts ...")
	accounts, err := fetchLocalAccounts(client, apiBase, sessionCookie)
	if err != nil {
		return fmt.Errorf("fetch accounts: %w", err)
	}
	fmt.Printf("found %d accounts\n", len(accounts))

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	var slim []slimAccount
	for i, acct := range accounts {
		if acct.Provider != "kiro" {
			continue
		}
		fmt.Printf("[%d/%d] %s ... ", i+1, len(accounts), acct.Email)

		rt, err := applyAndReadToken(client, apiBase, sessionCookie, acct.ID)
		if err != nil {
			fmt.Printf("SKIP (%v)\n", err)
			continue
		}
		fmt.Println("ok")

		slim = append(slim, slimAccount{
			Email:            acct.Email,
			ID:               acct.ID,
			Status:           acct.Status,
			RemainingCredits: acct.RemainingCredits,
			RefreshToken:     rt,
			PlanType:         acct.PlanType,
		})

		time.Sleep(200 * time.Millisecond)
	}

	files := map[string]func() error{
		"kiro-credentials.json":   func() error { return writeJSON(filepath.Join(outDir, "kiro-credentials.json"), slim) },
		"kiro-credentials.csv":    func() error { return writeCSV(filepath.Join(outDir, "kiro-credentials.csv"), slim) },
		"kiro-token-refresh.txt":  func() error { return writeTokenRefresh(filepath.Join(outDir, "kiro-token-refresh.txt"), slim) },
		"kiro-refresh-tokens.txt": func() error { return writeRefreshTokens(filepath.Join(outDir, "kiro-refresh-tokens.txt"), slim) },
	}
	for name, fn := range files {
		if err := fn(); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}

	fmt.Printf("\ndone: %d kiro accounts -> %s\n", len(slim), outDir)
	return nil
}

func runExtractExternal(stdin *bufio.Reader) error {
	sessionCookie := strings.TrimSpace(prompt(stdin, "enowxai_session cookie> ", ""))
	if sessionCookie == "" {
		return errors.New("empty session cookie")
	}
	apiBase := prompt(stdin, fmt.Sprintf("api base [%s]> ", defaultExternalAPIBase), defaultExternalAPIBase)
	outDir := prompt(stdin, fmt.Sprintf("output directory [%s]> ", defaultOutDir), defaultOutDir)

	fmt.Printf("\nfetching accounts from %s ...\n", apiBase)
	raw, err := fetchAccountsExternal(apiBase, sessionCookie, httpTimeout)
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

func runExtractFromEnv(stdin *bufio.Reader) error {
	env, _ := loadEnv(".env")
	sessionCookie := env["ENOWXAI_SESSION"]
	if sessionCookie == "" {
		return errors.New("ENOWXAI_SESSION not found in .env")
	}
	fmt.Printf("loaded session cookie from .env (len=%d)\n", len(sessionCookie))
	apiBase := env["API_BASE"]
	if apiBase == "" {
		apiBase = defaultExternalAPIBase
	}
	apiBase = prompt(stdin, fmt.Sprintf("api base [%s]> ", apiBase), apiBase)
	outDir := prompt(stdin, fmt.Sprintf("output directory [%s]> ", defaultOutDir), defaultOutDir)

	fmt.Printf("\nfetching accounts from %s ...\n", apiBase)
	raw, err := fetchAccountsExternal(apiBase, sessionCookie, httpTimeout)
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

func loginDashboard(client *http.Client, apiBase, password string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"password": password})
	url := strings.TrimRight(apiBase, "/") + "/api/auth/login"

	resp, err := client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	for _, c := range resp.Cookies() {
		if c.Name == defaultSessionCookieName {
			return c.Value, nil
		}
	}
	return "", errors.New("no session cookie in login response")
}

func fetchLocalAccounts(client *http.Client, apiBase, sessionCookie string) ([]localAccount, error) {
	url := strings.TrimRight(apiBase, "/") + defaultAccountsPath
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", defaultSessionCookieName+"="+sessionCookie)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var result localAccountsResp
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse accounts: %w", err)
	}
	return result.Accounts, nil
}

func applyAndReadToken(client *http.Client, apiBase, sessionCookie, accountID string) (string, error) {
	base := strings.TrimRight(apiBase, "/")

	// Step 1: Apply account (writes credentials to desktop auth file)
	applyPayload, _ := json.Marshal(map[string]string{"account_id": accountID})
	applyReq, err := http.NewRequest(http.MethodPost, base+"/api/accounts/apply", bytes.NewReader(applyPayload))
	if err != nil {
		return "", err
	}
	applyReq.Header.Set("Cookie", defaultSessionCookieName+"="+sessionCookie)
	applyReq.Header.Set("Content-Type", "application/json")

	applyResp, err := client.Do(applyReq)
	if err != nil {
		return "", err
	}
	defer applyResp.Body.Close()
	io.Copy(io.Discard, applyResp.Body)
	// Note: apply may return 500 when Kiro desktop is not installed, but the
	// token is still written to the desktop auth file. We proceed regardless.

	// Step 2: Read desktop token
	readReq, err := http.NewRequest(http.MethodGet, base+"/api/accounts/kiro-refresh-token/desktop", nil)
	if err != nil {
		return "", err
	}
	readReq.Header.Set("Cookie", defaultSessionCookieName+"="+sessionCookie)
	readReq.Header.Set("Accept", "application/json")

	readResp, err := client.Do(readReq)
	if err != nil {
		return "", err
	}
	defer readResp.Body.Close()
	body, err := io.ReadAll(readResp.Body)
	if err != nil {
		return "", err
	}
	if readResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("read HTTP %d: %s", readResp.StatusCode, truncate(string(body), 200))
	}

	var token desktopTokenResp
	if err := json.Unmarshal(body, &token); err != nil {
		return "", fmt.Errorf("parse token: %w", err)
	}
	if token.RefreshToken == "" {
		return "", errors.New("empty refresh token in response")
	}
	return token.RefreshToken, nil
}

func fetchAccountsExternal(apiBase, sessionCookie string, timeout time.Duration) ([]byte, error) {
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

func splitKiro(raw []byte) (full []json.RawMessage, slim []slimAccount, err error) {
	var resp credsResp
	if err = json.Unmarshal(raw, &resp); err == nil && len(resp.Data) > 0 {
		return splitKiroFromRaw(resp.Data)
	}
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
