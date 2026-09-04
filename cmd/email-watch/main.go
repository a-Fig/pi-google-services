package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sombi/pi-google-services/internal/auth"
	"github.com/sombi/pi-google-services/internal/config"
	"github.com/sombi/pi-google-services/internal/gmail"
)

type ruleFile struct {
	Lookback string `json:"lookback"`
	Rules    []struct {
		ID          string   `json:"id"`
		Description string   `json:"description"`
		Query       string   `json:"query"`
		AuthoredBy  []string `json:"authoredBy"`
	} `json:"rules"`
}

func mailbox(value string) string {
	address, err := mail.ParseAddress(strings.TrimSpace(value))
	if err == nil {
		return strings.ToLower(address.Address)
	}
	return strings.ToLower(strings.Trim(strings.TrimSpace(value), "<>"))
}

func authoredMessage(message *gmail.EmailSummary, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	want := map[string]bool{}
	for _, address := range allowed {
		want[strings.ToLower(address)] = true
	}
	return message.XForwardedTo == "" && want[mailbox(message.From)] && want[mailbox(message.ReturnPath)]
}

func classifyOrigin(message *gmail.EmailSummary) {
	subject := strings.ToLower(strings.TrimSpace(message.Subject))
	if strings.HasPrefix(subject, "fwd:") || strings.HasPrefix(subject, "fw:") {
		message.Origin = "manual-forward"
	} else {
		message.Origin = "direct"
	}
}

type result struct {
	RuleID      string                `json:"rule_id"`
	Description string                `json:"description"`
	Messages    []*gmail.EmailSummary `json:"messages"`
	Error       string                `json:"error,omitempty"`
}

func credentials() ([]byte, error) {
	if path := os.Getenv("GOOGLE_OAUTH_CREDENTIALS"); path != "" {
		return os.ReadFile(path)
	}
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(dir, config.CredFile))
}

func service(ctx context.Context) (*gmail.Service, error) {
	data, err := credentials()
	if err != nil {
		return nil, fmt.Errorf("credentials: %w", err)
	}
	creds, err := config.LoadCredentialsFromBytes(data)
	if err != nil {
		return nil, err
	}
	tok, err := auth.LoadToken()
	if err != nil {
		return nil, fmt.Errorf("token: %w", err)
	}
	if tok == nil {
		return nil, fmt.Errorf("Google account is not authenticated")
	}
	return gmail.New(ctx, auth.NewFromCredentials(creds, []string{"https://www.googleapis.com/auth/gmail.readonly"}).CachedTokenSource(ctx, tok))
}

func main() {
	path := ".pi/email-wake-rules.json"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	var cfg ruleFile
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf(`{"error":%q}\n`, err.Error())
		os.Exit(1)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		fmt.Printf(`{"error":%q}\n`, err.Error())
		os.Exit(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	svc, err := service(ctx)
	if err != nil {
		fmt.Printf(`{"error":%q}\n`, err.Error())
		os.Exit(1)
	}
	for _, rule := range cfg.Rules {
		query := rule.Query
		if cfg.Lookback != "" {
			query += " " + cfg.Lookback
		}
		messages, err := svc.SearchEmails(ctx, query, 50)
		if err == nil && len(rule.AuthoredBy) > 0 {
			filtered := messages[:0]
			for _, message := range messages {
				if authoredMessage(message, rule.AuthoredBy) {
					classifyOrigin(message)
					filtered = append(filtered, message)
				}
			}
			messages = filtered
		}
		item := result{RuleID: rule.ID, Description: rule.Description, Messages: messages}
		if err != nil {
			item.Error = err.Error()
		}
		encoded, _ := json.Marshal(item)
		fmt.Println(string(encoded))
	}
}
