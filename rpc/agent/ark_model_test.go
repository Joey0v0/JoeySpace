package agent

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestArkConfigRequiresNonSecretEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name string
		vars map[string]string
	}{
		{"missing both", nil},
		{"missing model", map[string]string{"ARK_API_KEY": "test-key"}},
		{"invalid model", map[string]string{"ARK_API_KEY": "test-key", "ARK_MODEL_ID": "two words"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := arkConfigFromEnv(func(key string) string { return tc.vars[key] })
			if cfg != nil || status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("config = %v, error = %v", cfg, err)
			}
		})
	}
}

func TestArkConfigBoundsOneAsk(t *testing.T) {
	vars := map[string]string{"ARK_API_KEY": "test-key", "ARK_MODEL_ID": "ep-test-only"}
	cfg, err := arkConfigFromEnv(func(key string) string { return vars[key] })
	if err != nil || cfg.APIKey != "test-key" || cfg.Model != "ep-test-only" ||
		cfg.Timeout == nil || *cfg.Timeout != arkRequestTimeout ||
		cfg.MaxTokens == nil || *cfg.MaxTokens != arkMaxOutputTokens ||
		cfg.RetryTimes == nil || *cfg.RetryTimes != 0 {
		t.Fatalf("unexpected Ark config: %v, %v", cfg, err)
	}
}

func TestArkGeneratorConstructionDoesNotRequireProviderCall(t *testing.T) {
	t.Setenv("ARK_API_KEY", "test-key")
	t.Setenv("ARK_MODEL_ID", "ep-test-only")
	generator, err := NewArkGroupReplyGenerator(context.Background())
	if err != nil || generator == nil {
		t.Fatalf("construct Ark generator = %v, %v", generator, err)
	}
	draftGenerator, err := NewArkTaskDraftGenerator(context.Background())
	if err != nil || draftGenerator == nil {
		t.Fatalf("construct Ark draft generator = %v, %v", draftGenerator, err)
	}
}
