package webhooks

import "context"

// testSecretResolver is a test double for SecretResolver: a static
// reference→secret map, so tests never touch the environment.
type testSecretResolver map[string]string

func (r testSecretResolver) ResolveWebhookSecret(_ context.Context, secretID string) ([]byte, bool, error) {
	value := r[secretID]
	if value == "" {
		return nil, false, nil
	}
	return []byte(value), true, nil
}
