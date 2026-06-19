package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/mcmoney/platform-control-plane/internal/auth"
	"github.com/mcmoney/platform-control-plane/internal/domain"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}

	switch args[0] {
	case "classes":
		api, _, err := extractAPI(args[1:])
		if err != nil {
			return err
		}
		return doJSONRequest(http.MethodGet, api+"/v1/environment-classes", nil, requestHeaders(args[1:]))
	case "request":
		return runRequest(args[1:])
	case "token":
		return runToken(args[1:])
	default:
		return usage()
	}
}

func runRequest(args []string) error {
	if len(args) == 0 {
		return usage()
	}

	switch args[0] {
	case "list":
		api, _, err := extractAPI(args[1:])
		if err != nil {
			return err
		}
		return doJSONRequest(http.MethodGet, api+"/v1/environment-requests", nil, requestHeaders(args[1:]))
	case "get":
		api, rest, err := extractAPI(args[1:])
		if err != nil {
			return err
		}
		id, err := requiredFlag(rest, "--id")
		if err != nil {
			return err
		}
		return doJSONRequest(http.MethodGet, api+"/v1/environment-requests/"+id, nil, requestHeaders(args[1:]))
	case "approve":
		api, rest, err := extractAPI(args[1:])
		if err != nil {
			return err
		}
		id, err := requiredFlag(rest, "--id")
		if err != nil {
			return err
		}
		headers := requestHeaders(args[1:])
		req, err := fetchRequest(api, id, headers)
		if err != nil {
			return err
		}
		secret := approvalSecret(rest)
		if secret != "" {
			actor, err := actorFromToken(headers["Authorization"])
			if err != nil {
				return err
			}
			headers[auth.HeaderApprovalSignature] = computeApprovalSignature(secret, id, actor, req.Class)
		}
		return doJSONRequest(http.MethodPost, api+"/v1/environment-requests/"+id+"/approve", nil, headers)
	case "reconcile":
		api, rest, err := extractAPI(args[1:])
		if err != nil {
			return err
		}
		id, err := requiredFlag(rest, "--id")
		if err != nil {
			return err
		}
		return doJSONRequest(http.MethodPost, api+"/v1/environment-requests/"+id+"/reconcile", nil, requestHeaders(args[1:]))
	case "wait":
		api, rest, err := extractAPI(args[1:])
		if err != nil {
			return err
		}
		id, err := requiredFlag(rest, "--id")
		if err != nil {
			return err
		}
		timeoutSeconds, _ := strconv.Atoi(optionalValueOrDefault(rest, "--timeout", "60"))
		return waitForCompletion(api, id, requestHeaders(args[1:]), time.Duration(timeoutSeconds)*time.Second)
	case "create":
		api, rest, err := extractAPI(args[1:])
		if err != nil {
			return err
		}

		ttlValue, err := requiredFlag(rest, "--ttl")
		if err != nil {
			return err
		}
		ttl, err := strconv.Atoi(ttlValue)
		if err != nil {
			return fmt.Errorf("invalid --ttl value %q: %w", ttlValue, err)
		}

		body := domain.CreateRequestInput{
			App:        mustFlag(rest, "--app"),
			Team:       mustFlag(rest, "--team"),
			Class:      mustFlag(rest, "--class"),
			Region:     mustFlag(rest, "--region"),
			TTLHours:   ttl,
			Owner:      mustFlag(rest, "--owner"),
			Repository: mustFlag(rest, "--repo"),
			Revision:   optionalValueOrDefault(rest, "--revision", "main"),
			Labels:     parseLabels(optionalFlag(rest, "--labels")),
		}
		if body.App == "" || body.Team == "" || body.Class == "" || body.Region == "" || body.Owner == "" || body.Repository == "" {
			return errors.New("create requires --app --team --class --region --ttl --owner --repo")
		}

		return doJSONRequest(http.MethodPost, api+"/v1/environment-requests", body, requestHeaders(args[1:]))
	default:
		return usage()
	}
}

func runToken(args []string) error {
	if len(args) == 0 || args[0] != "mint" {
		return usage()
	}

	secret := optionalValueOrDefault(args[1:], "--secret", os.Getenv("PLATFORM_JWT_HS256_SECRET"))
	if secret == "" {
		return errors.New("token mint requires --secret or PLATFORM_JWT_HS256_SECRET")
	}
	subject := optionalValueOrDefault(args[1:], "--subject", "platformctl")
	role := optionalValueOrDefault(args[1:], "--role", "admin")
	issuer := optionalValueOrDefault(args[1:], "--issuer", os.Getenv("PLATFORM_OIDC_ISSUER_URL"))
	audience := optionalValueOrDefault(args[1:], "--audience", os.Getenv("PLATFORM_OIDC_AUDIENCE"))
	actor := optionalValueOrDefault(args[1:], "--actor", subject)
	ttlSeconds, err := strconv.Atoi(optionalValueOrDefault(args[1:], "--ttl", "3600"))
	if err != nil {
		return fmt.Errorf("invalid --ttl value: %w", err)
	}

	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"sub":   subject,
		"email": actor,
		"role":  role,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Duration(ttlSeconds) * time.Second).Unix(),
	}
	if issuer != "" {
		claims["iss"] = issuer
	}
	if audience != "" {
		claims["aud"] = audience
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return err
	}
	fmt.Println(signed)
	return nil
}

func doJSONRequest(method, url string, body any, headers map[string]string) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("request failed with %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}

	fmt.Println(string(payload))
	return nil
}

func fetchRequest(api, id string, headers map[string]string) (domain.EnvironmentRequest, error) {
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(api, "/")+"/v1/environment-requests/"+id, nil)
	if err != nil {
		return domain.EnvironmentRequest{}, err
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return domain.EnvironmentRequest{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		payload, _ := io.ReadAll(resp.Body)
		return domain.EnvironmentRequest{}, fmt.Errorf("fetch request failed with %s: %s", resp.Status, strings.TrimSpace(string(payload)))
	}
	var out domain.EnvironmentRequest
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return domain.EnvironmentRequest{}, err
	}
	return out, nil
}

func waitForCompletion(api, id string, headers map[string]string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, err := fetchRequest(api, id, headers)
		if err != nil {
			return err
		}
		switch req.Status {
		case domain.StatusReady, domain.StatusFailed:
			data, err := json.Marshal(req)
			if err != nil {
				return err
			}
			fmt.Println(string(data))
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("timed out waiting for request %s", id)
}

func extractAPI(args []string) (string, []string, error) {
	api := optionalFlag(args, "--api")
	if api == "" {
		api = "http://localhost:8080"
	}

	return strings.TrimRight(api, "/"), args, nil
}

func optionalFlag(args []string, name string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func optionalValueOrDefault(args []string, name, fallback string) string {
	value := optionalFlag(args, name)
	if value == "" {
		return fallback
	}
	return value
}

func requiredFlag(args []string, name string) (string, error) {
	value := optionalFlag(args, name)
	if value == "" {
		return "", fmt.Errorf("missing required flag %s", name)
	}
	return value, nil
}

func mustFlag(args []string, name string) string {
	value, _ := requiredFlag(args, name)
	return value
}

func parseLabels(raw string) map[string]string {
	if raw == "" {
		return nil
	}

	out := map[string]string{}
	for _, pair := range strings.Split(raw, ",") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}

	return out
}

func usage() error {
	return errors.New(`usage:
  platformctl token mint [--subject app-team] [--actor app-team@example.com] [--role requester] [--issuer local-dev] [--audience platform-control-plane] [--ttl 3600] [--secret secret]
  platformctl classes [--api http://localhost:8080] [--token $TOKEN]
  platformctl request list [--api http://localhost:8080] [--token $TOKEN]
  platformctl request get --id <request-id> [--api http://localhost:8080] [--token $TOKEN]
  platformctl request approve --id <request-id> [--api http://localhost:8080] [--token $TOKEN] [--approval-secret secret]
  platformctl request reconcile --id <request-id> [--api http://localhost:8080] [--token $TOKEN]
  platformctl request wait --id <request-id> [--timeout 60] [--api http://localhost:8080] [--token $TOKEN]
  platformctl request create --app <app> --team <team> --class <class> --region <region> --ttl <hours> --owner <owner> --repo <git-url> [--revision main] [--labels k=v,k2=v2] [--api http://localhost:8080] [--token $TOKEN]`)
}

func requestHeaders(args []string) map[string]string {
	token := optionalValueOrDefault(args, "--token", os.Getenv("PLATFORM_TOKEN"))
	headers := map[string]string{}
	if token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	return headers
}

func approvalSecret(args []string) string {
	value := optionalFlag(args, "--approval-secret")
	if value == "" {
		value = os.Getenv("PLATFORM_APPROVAL_HMAC_SECRET")
	}
	return value
}

func computeApprovalSignature(secret, requestID, actor, class string) string {
	return auth.ComputeApprovalSignature(secret, requestID, actor, class)
}

func actorFromToken(authorization string) (string, error) {
	token := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer"))
	if token == "" {
		return "", errors.New("missing bearer token for approval signature")
	}
	claims := jwt.MapClaims{}
	parsed, _, err := new(jwt.Parser).ParseUnverified(token, claims)
	if err != nil {
		return "", fmt.Errorf("parse token claims: %w", err)
	}
	mapClaims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return "", errors.New("unexpected token claims")
	}
	if email, ok := mapClaims["email"].(string); ok && strings.TrimSpace(email) != "" {
		return strings.TrimSpace(email), nil
	}
	if sub, ok := mapClaims["sub"].(string); ok && strings.TrimSpace(sub) != "" {
		return strings.TrimSpace(sub), nil
	}
	return "", errors.New("token missing actor claim")
}
