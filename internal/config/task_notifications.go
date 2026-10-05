package config

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
)

type NotificationTLSConfig struct {
	CertFile string `mapstructure:"cert_file"`
	KeyFile  string `mapstructure:"key_file"`
	CAFile   string `mapstructure:"ca_file"`
}

type TaskNotificationsConfig struct {
	Push TaskNotificationPushConfig `mapstructure:"push"`
	WS   TaskNotificationWSConfig   `mapstructure:"ws"`
}

type TaskNotificationPushConfig struct {
	Enabled       bool                  `mapstructure:"enabled"`
	Topic         string                `mapstructure:"topic"`
	ConsumerGroup string                `mapstructure:"consumer_group"`
	WSDNSName     string                `mapstructure:"ws_dns_name"`
	Routes        map[string]string     `mapstructure:"routes"`
	TLS           NotificationTLSConfig `mapstructure:"tls"`
}

type TaskNotificationWSConfig struct {
	Enabled     bool                  `mapstructure:"enabled"`
	ListenAddr  string                `mapstructure:"listen_addr"`
	PushDNSName string                `mapstructure:"push_dns_name"`
	UserRPCAddr string                `mapstructure:"user_rpc_addr"`
	TLS         NotificationTLSConfig `mapstructure:"tls"`
}

func notificationName(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 249 {
		return false
	}
	for _, ch := range value {
		if ch != '.' && ch != '_' && ch != '-' && (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
			return false
		}
	}
	return true
}

func notificationHostPort(value string, allowEmptyHost bool) (string, int, bool) {
	host, port, err := net.SplitHostPort(value)
	n, parseErr := strconv.Atoi(port)
	if err != nil || parseErr != nil || n < 1 || n > 65535 || strconv.Itoa(n) != port || net.JoinHostPort(host, port) != value {
		return "", 0, false
	}
	if host == "" {
		return host, n, allowEmptyHost
	}
	if net.ParseIP(host) != nil {
		return host, n, true
	}
	if len(host) > 253 {
		return "", 0, false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", 0, false
		}
		for _, ch := range label {
			if ch != '-' && (ch < 'a' || ch > 'z') && (ch < 'A' || ch > 'Z') && (ch < '0' || ch > '9') {
				return "", 0, false
			}
		}
	}
	return host, n, true
}

func notificationServiceName(name string) bool {
	_, _, valid := notificationHostPort(net.JoinHostPort(name, "443"), false)
	return valid && net.ParseIP(name) == nil
}

func notificationTLSFiles(files NotificationTLSConfig) bool {
	for _, value := range []string{files.CertFile, files.KeyFile, files.CAFile} {
		if value == "" || strings.TrimSpace(value) != value {
			return false
		}
	}
	return true
}

// Only validate this process's enabled role. Certificate contents are loaded by
// its runtime constructor, before any infrastructure is connected.
func ValidateTaskNotificationPush(cfg *Config) error {
	if cfg == nil {
		return errors.New("task notification Push configuration required")
	}
	c := cfg.TaskNotifications.Push
	if !c.Enabled {
		return nil
	}
	invalid := errors.New("invalid enabled task notification Push configuration")
	if !notificationName(c.Topic) || !notificationName(c.ConsumerGroup) || c.ConsumerGroup == cfg.Kafka.ConsumerGroup ||
		!notificationName(cfg.Kafka.TopicChat) || c.Topic == cfg.Kafka.TopicChat || c.Topic == cfg.Kafka.TopicAgentTrigger || c.Topic == "agent_task_triggers" ||
		!notificationServiceName(c.WSDNSName) || !notificationTLSFiles(c.TLS) || len(c.Routes) == 0 || len(cfg.Kafka.Brokers) == 0 {
		return invalid
	}
	for _, broker := range cfg.Kafka.Brokers {
		if _, _, ok := notificationHostPort(broker, false); !ok {
			return invalid
		}
	}
	for addr, origin := range c.Routes {
		if _, _, ok := notificationHostPort(addr, false); !ok {
			return invalid
		}
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.Opaque != "" || u.String() != origin {
			return invalid
		}
		if _, _, ok := notificationHostPort(u.Host, false); !ok {
			return invalid
		}
	}
	return nil
}

func ValidateTaskNotificationWS(cfg *Config) error {
	if cfg == nil {
		return errors.New("task notification WS configuration required")
	}
	c := cfg.TaskNotifications.WS
	if !c.Enabled {
		return nil
	}
	invalid := errors.New("invalid enabled task notification WS configuration")
	_, port, ok := notificationHostPort(c.ListenAddr, true)
	if !ok || port == cfg.WSServer.Port || port == cfg.WSServer.RPCPort || !notificationServiceName(c.PushDNSName) || !notificationTLSFiles(c.TLS) {
		return invalid
	}
	if _, _, ok := notificationHostPort(c.UserRPCAddr, false); !ok {
		return invalid
	}
	return nil
}
