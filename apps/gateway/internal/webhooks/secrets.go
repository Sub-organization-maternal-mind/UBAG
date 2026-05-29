package webhooks

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
)

type SecretResolver interface {
	ResolveWebhookSecret(ctx context.Context, secretID string) ([]byte, bool, error)
}

type EnvSecretResolver struct {
	DefaultSecret string
	EnvPrefix     string
}

func NewEnvSecretResolver(defaultSecret string, envPrefix string) EnvSecretResolver {
	if strings.TrimSpace(envPrefix) == "" {
		envPrefix = "UBAG_WEBHOOK_SECRET_"
	}
	return EnvSecretResolver{DefaultSecret: defaultSecret, EnvPrefix: envPrefix}
}

func (r EnvSecretResolver) ResolveWebhookSecret(_ context.Context, secretID string) ([]byte, bool, error) {
	secretID = strings.TrimSpace(secretID)
	if secretID == "" {
		return nil, false, fmt.Errorf("webhook secret id is required")
	}
	key := r.EnvPrefix + secretEnvSuffix(secretID)
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return []byte(value), true, nil
	}
	if strings.TrimSpace(r.DefaultSecret) != "" {
		return []byte(strings.TrimSpace(r.DefaultSecret)), true, nil
	}
	return nil, false, nil
}

type StaticSecretResolver map[string]string

func (r StaticSecretResolver) ResolveWebhookSecret(_ context.Context, secretID string) ([]byte, bool, error) {
	value := strings.TrimSpace(r[strings.TrimSpace(secretID)])
	if value == "" {
		return nil, false, nil
	}
	return []byte(value), true, nil
}

func secretEnvSuffix(secretID string) string {
	value := regexp.MustCompile(`[^A-Za-z0-9]+`).ReplaceAllString(secretID, "_")
	value = strings.Trim(value, "_")
	return strings.ToUpper(value)
}
