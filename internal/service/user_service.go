package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/yjydist/go-im/internal/config"
	"github.com/yjydist/go-im/internal/model"
	"github.com/yjydist/go-im/internal/pkg/errcode"
	pkgjwt "github.com/yjydist/go-im/internal/pkg/jwt"
	"github.com/yjydist/go-im/internal/pkg/snowflake"
	"github.com/yjydist/go-im/internal/repository"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"
)

// UserService 用户服务接口
type UserService interface {
	Register(ctx context.Context, username, password, nickname string) error
	Login(ctx context.Context, username, password string) (token string, err error)
	GetUserInfo(ctx context.Context, userID int64) (*model.User, error)
}

type userService struct {
	userRepo repository.UserRepository
	logger   *zap.Logger
}

// NewUserService 创建用户服务
func NewUserService(logger *zap.Logger) UserService {
	return &userService{
		userRepo: repository.NewUserRepository(),
		logger:   logger,
	}
}

func (s *userService) Register(ctx context.Context, username, password, nickname string) error {
	// 检查用户名是否已存在
	_, err := s.userRepo.GetByUsername(ctx, username)
	if err == nil {
		return fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrUserExist)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("check username failed: %w", err)
	}

	// bcrypt 加密密码
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("hash password failed: %w", err)
	}

	user := &model.User{
		ID:       snowflake.GenID(),
		Username: username,
		Password: string(hashedPassword),
		Nickname: nickname,
		Status:   1,
	}

	if err := s.userRepo.Create(ctx, user); err != nil {
		return err
	}

	s.logger.Info("user registered", zap.String("username", username), zap.Int64("user_id", user.ID))
	return nil
}

func (s *userService) Login(ctx context.Context, username, password string) (string, error) {
	user, err := s.userRepo.GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrUserNotFound)
		}
		return "", fmt.Errorf("query user failed: %w", err)
	}

	// 检查用户状态
	if user.Status == 2 {
		return "", fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrUserBanned)
	}

	// 验证密码
	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		return "", fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrPasswordWrong)
	}

	// 生成 JWT Token
	cfg := config.GlobalConfig
	token, err := pkgjwt.GenerateToken(user.ID, cfg.JWT.Secret, cfg.JWT.AccessExpireHours)
	if err != nil {
		return "", fmt.Errorf("generate token failed: %w", err)
	}

	s.logger.Info("user logged in", zap.String("username", username), zap.Int64("user_id", user.ID))
	return token, nil
}

func (s *userService) GetUserInfo(ctx context.Context, userID int64) (*model.User, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: %d", ErrBusiness, errcode.ErrUserNotFound)
		}
		return nil, fmt.Errorf("query user failed: %w", err)
	}
	return user, nil
}
