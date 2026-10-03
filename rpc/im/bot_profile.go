package main

import (
	"context"
	"errors"
	"strings"

	"github.com/yjydist/go-im/internal/model"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/gorm"
)

// The code is deployment configuration, never a request or model parameter.
func lookupEnabledBot(ctx context.Context, db *gorm.DB, code string) (model.IMBot, error) {
	if db == nil || code == "" || len(code) > 32 || strings.TrimSpace(code) != code {
		return model.IMBot{}, status.Error(codes.Unavailable, "IM bot is not configured")
	}
	var bot model.IMBot
	err := db.WithContext(ctx).Select("id", "code", "display_name", "status").Where("code = ?", code).Take(&bot).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return model.IMBot{}, status.Error(codes.FailedPrecondition, "IM bot profile is not provisioned")
	}
	if err != nil {
		return model.IMBot{}, teamGroupDBError(ctx, err)
	}
	if bot.ID <= 0 || bot.Status != 1 || strings.TrimSpace(bot.DisplayName) == "" {
		return model.IMBot{}, status.Error(codes.FailedPrecondition, "IM bot is disabled or invalid")
	}
	return bot, nil
}
