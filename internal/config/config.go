package config

import (
	"fmt"

	"github.com/spf13/viper"
)

// Config 统一配置结构体
type Config struct {
	App       AppConfig       `mapstructure:"app"`
	APIServer APIServerConfig `mapstructure:"api_server"`
	WSServer  WSServerConfig  `mapstructure:"ws_server"`
	MySQL     MySQLConfig     `mapstructure:"mysql"`
	Redis     RedisConfig     `mapstructure:"redis"`
	Kafka     KafkaConfig     `mapstructure:"kafka"`
	JWT       JWTConfig       `mapstructure:"jwt"`
	Log       LogConfig       `mapstructure:"log"`
}

type AppConfig struct {
	Env      string `mapstructure:"env"`
	ServerID int64  `mapstructure:"server_id"`
}

type APIServerConfig struct {
	Port int `mapstructure:"port"`
}

type WSServerConfig struct {
	Port    int `mapstructure:"port"`
	RPCPort int `mapstructure:"rpc_port"`
}

type MySQLConfig struct {
	DSN          string `mapstructure:"dsn"`
	MaxOpenConns int    `mapstructure:"max_open_conns"`
	MaxIdleConns int    `mapstructure:"max_idle_conns"`
}

type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type KafkaConfig struct {
	Brokers       []string `mapstructure:"brokers"`
	TopicChat     string   `mapstructure:"topic_chat"`
	ConsumerGroup string   `mapstructure:"consumer_group"`
}

type JWTConfig struct {
	Secret             string `mapstructure:"secret"`
	AccessExpireHours  int    `mapstructure:"access_expire_hours"`
	RefreshExpireHours int    `mapstructure:"refresh_expire_hours"`
}

type LogConfig struct {
	Level    string `mapstructure:"level"`
	Filename string `mapstructure:"filename"`
}

// GlobalConfig 全局配置实例
var GlobalConfig *Config

// Load 加载配置文件
func Load(configPath string) (*Config, error) {
	viper.SetConfigFile(configPath)
	viper.SetConfigType("yaml")

	if err := viper.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config file failed: %w", err)
	}

	cfg := &Config{}
	if err := viper.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config failed: %w", err)
	}

	GlobalConfig = cfg
	return cfg, nil
}
