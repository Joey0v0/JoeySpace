package agent

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/ark"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	arkRequestTimeout      = 15 * time.Second
	arkDraftRequestTimeout = 45 * time.Second
	arkMaxOutputTokens     = 1024
	arkRetryTimes          = 0
)

// NewArkGroupReplyGenerator configures the selected provider. Construction
// does not send a model request; Generate is only called after IM authorization.
func NewArkGroupReplyGenerator(ctx context.Context) (*EinoGroupReplyGenerator, error) {
	cfg, err := arkConfigFromEnv(os.Getenv)
	if err != nil {
		return nil, err
	}
	chatModel, err := ark.NewChatModel(ctx, cfg)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "Ark model is unavailable")
	}
	return NewEinoGroupReplyGenerator(ctx, chatModel)
}

// NewArkTaskDraftGenerator allows longer structured output than a direct Ask.
// Construction does not send a model request.
func NewArkTaskDraftGenerator(ctx context.Context) (*EinoTaskDraftGenerator, error) {
	cfg, err := arkDraftConfigFromEnv(os.Getenv)
	if err != nil {
		return nil, err
	}
	chatModel, err := ark.NewChatModel(ctx, cfg)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "Ark model is unavailable")
	}
	return NewEinoTaskDraftGenerator(ctx, chatModel)
}

func arkDraftConfigFromEnv(getenv func(string) string) (*ark.ChatModelConfig, error) {
	cfg, err := arkConfigFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	timeout := arkDraftRequestTimeout
	cfg.Timeout = &timeout
	return cfg, nil
}

func arkConfigFromEnv(getenv func(string) string) (*ark.ChatModelConfig, error) {
	apiKey := strings.TrimSpace(getenv("ARK_API_KEY"))
	modelID := strings.TrimSpace(getenv("ARK_MODEL_ID"))
	if apiKey == "" || strings.ContainsAny(apiKey, "\r\n") {
		return nil, status.Error(codes.FailedPrecondition, "ARK_API_KEY is required")
	}
	if modelID == "" || strings.ContainsAny(modelID, " \t\r\n") {
		return nil, status.Error(codes.FailedPrecondition, "ARK_MODEL_ID is required")
	}
	timeout, maxTokens, retries := arkRequestTimeout, arkMaxOutputTokens, arkRetryTimes
	return &ark.ChatModelConfig{
		APIKey: apiKey, Model: modelID, Timeout: &timeout,
		MaxTokens: &maxTokens, RetryTimes: &retries,
	}, nil
}
