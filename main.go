// Command kiro-extract pulls all Kiro provider credentials from an enowxai
// proxy daemon by reading its auth token (proxy JWT) and calling the upstream
// enowxlabs API.
//
// Source modes:
//   -mode docker   read auth.json from a running enowxai container
//   -mode host     read auth.json from a path on the local filesystem
//
// Outputs (under -out dir, default ./kiro-out):
//   all-credentials.json    raw upstream response (every provider)
//   kiro-full.json          full record for every kiro account
//   kiro-credentials.json   slim per-account record
//   kiro-credentials.csv    same as csv
//   kiro-token-refresh.txt  accessToken:refreshToken per line
//   kiro-refresh-tokens.txt refreshToken per line
//
// Cross platform: pure stdlib, works on linux/mac/windows. The docker mode
// shells out to the `docker` CLI which must be on PATH.
package main

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	defaultAPIBase       = "https://api.enowxlabs.com"
	defaultContainer     = "enowxai"
	defaultContainerPath = "/root/.enowxai/auth.json"
)

type authFile struct {
	LicenseKey string `json:"license_key"`
	Token      string `json:"token"`
	Tier       string `json:"tier,omitempty"`
	MachineID  string `json:"machine_id,omitempty"`
	IsVIP      bool   `json:"is_vip,omitempty"`
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

// account uses pointer strings so JSON null doesn't fail decoding. Only
// fields we actually emit are listed; everything else is dropped.
type account struct {
	ID               string      `json:"id"`
	Email            string      `json:"email"`
	Provider         string      `json:"provider"`
	Status           string      `json:"status"`
	RemainingCredits float64     `json:"remaining_credits"`
	LastRefreshAt    *string     `json:"last_refresh_at"`
	Credentials      credentials `json:"credentials"`
}

func strVal(p *string) string {
	if p == nil {
		return ""
	}
	return *p
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

func main() {
	var (
		mode      = flag.String("mode", "docker", "source of auth.json: docker | host")
		container = flag.String("container", defaultContainer, "docker container name (mode=docker)")
		cPath     = flag.String("container-path", defaultContainerPath, "path to auth.json inside the container (mode=docker)")
		hostPath  = flag.String("path", "", "path to auth.json on the host (mode=host); defaults to platform-specific")
		token     = flag.String("token", "", "proxy JWT to use directly (skips reading auth.json)")
		apiBase   = flag.String("api", defaultAPIBase, "upstream API base URL")
		outDir    = flag.String("out", "kiro-out", "output directory")
		timeout   = flag.Duration("timeout", 30*time.Second, "HTTP timeout")
		quiet     = flag.Bool("quiet", false, "suppress progress output")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [flags]\n\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "Extract Kiro credentials managed by an enowxai proxy daemon.")
		fmt.Fprintln(os.Stderr, "\nFlags:")
		flag.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nExamples:")
		fmt.Fprintln(os.Stderr, "  kiro-extract -mode docker -container enowxai")
		fmt.Fprintln(os.Stderr, "  kiro-extract -mode host -path ~/.enowxai/auth.json")
		fmt.Fprintln(os.Stderr, "  kiro-extract -token eyJhbGci... -out creds")
	}
	flag.Parse()

	logf := func(format string, a ...any) {
		if !*quiet {
			fmt.Fprintf(os.Stderr, format+"\n", a...)
		}
	}

	jwt := strings.TrimSpace(*token)
	if jwt == "" {
		var err error
		switch strings.ToLower(*mode) {
		case "docker":
			logf("reading auth.json from container %s:%s", *container, *cPath)
			jwt, err = tokenFromDocker(*container, *cPath)
		case "host":
			p := *hostPath
			if p == "" {
				p = defaultHostAuthPath()
			}
			logf("reading auth.json from %s", p)
			jwt, err = tokenFromHost(p)
		default:
			fail(fmt.Errorf("unknown -mode %q (want docker|host)", *mode))
		}
		if err != nil {
			fail(err)
		}
	}
	if jwt == "" {
		fail(errors.New("empty proxy token"))
	}

	logf("fetching credentials from %s", *apiBase)
	raw, err := fetchCredentials(*apiBase, jwt, *timeout)
	if err != nil {
		fail(err)
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fail(err)
	}

	rawPath := filepath.Join(*outDir, "all-credentials.json")
	if err := os.WriteFile(rawPath, raw, 0o600); err != nil {
		fail(err)
	}
	logf("wrote %s (%d bytes)", rawPath, len(raw))

	kiroFull, kiroSlim, err := splitKiro(raw)
	if err != nil {
		fail(err)
	}

	if err := writeJSON(filepath.Join(*outDir, "kiro-full.json"), kiroFull); err != nil {
		fail(err)
	}
	if err := writeJSON(filepath.Join(*outDir, "kiro-credentials.json"), kiroSlim); err != nil {
		fail(err)
	}
	if err := writeCSV(filepath.Join(*outDir, "kiro-credentials.csv"), kiroSlim); err != nil {
		fail(err)
	}
	if err := writeTokenRefresh(filepath.Join(*outDir, "kiro-token-refresh.txt"), kiroSlim); err != nil {
		fail(err)
	}
	if err := writeRefreshTokens(filepath.Join(*outDir, "kiro-refresh-tokens.txt"), kiroSlim); err != nil {
		fail(err)
	}

	logf("done: %d kiro accounts -> %s", len(kiroSlim), *outDir)
	fmt.Println(*outDir)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// tokenFromDocker shells out to `docker exec` to read auth.json.
func tokenFromDocker(container, path string) (string, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return "", fmt.Errorf("docker CLI not found on PATH: %w", err)
	}
	// `cat` is available on the enowxai image; fall back to `sh -c` for safety.
	cmd := exec.Command("docker", "exec", container, "sh", "-c", "cat "+shellQuote(path))
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("docker exec failed: %s: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("docker exec failed: %w", err)
	}
	return parseAuthToken(out)
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

// defaultHostAuthPath returns the most likely auth.json path for each OS when
// the daemon runs natively on the host.
func defaultHostAuthPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	switch runtime.GOOS {
	case "windows":
		// Windows binary stores config under %USERPROFILE%\.enowxai
		return filepath.Join(home, ".enowxai", "auth.json")
	default:
		return filepath.Join(home, ".enowxai", "auth.json")
	}
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

// shellQuote single-quotes a path for `sh -c`.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func fetchCredentials(apiBase, jwt string, timeout time.Duration) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(apiBase, "/")+"/proxy/accounts/credentials", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kiro-extract/1.0")

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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func splitKiro(raw []byte) (full []json.RawMessage, slim []slimAccount, err error) {
	var resp credsResp
	if err = json.Unmarshal(raw, &resp); err != nil {
		return nil, nil, fmt.Errorf("decode response: %w", err)
	}
	var skipped int
	for _, item := range resp.Data {
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
