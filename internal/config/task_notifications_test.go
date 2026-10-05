package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

func resetNotificationConfigForTest(t *testing.T) {
	t.Helper()
	viper.Reset()
	previous := GlobalConfig
	GlobalConfig = nil
	t.Cleanup(func() { viper.Reset(); GlobalConfig = previous })
}

func TestNotificationRepositoryTemplatesStayDisabledAndCanBeExplicitlyEnabled(t *testing.T) {
	for _, path := range []string{"../../config/go-im.yaml", "../../deploy/docker-config.yaml"} {
		t.Run(path, func(t *testing.T) {
			resetNotificationConfigForTest(t)
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.TaskNotifications.Push.Enabled || cfg.TaskNotifications.WS.Enabled {
				t.Fatal("repository template enabled online reminders by default")
			}
			cfg.TaskNotifications.Push.Enabled = true
			cfg.TaskNotifications.WS.Enabled = true
			// Local template intentionally leaves private credential paths empty.
			for _, files := range []*NotificationTLSConfig{&cfg.TaskNotifications.Push.TLS, &cfg.TaskNotifications.WS.TLS} {
				if files.CertFile == "" {
					*files = NotificationTLSConfig{CertFile: "cert.pem", KeyFile: "key.pem", CAFile: "ca.pem"}
				}
			}
			if err := ValidateTaskNotificationPush(cfg); err != nil {
				t.Fatal(err)
			}
			if err := ValidateTaskNotificationWS(cfg); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func validNotificationConfigForTest() *Config {
	return &Config{
		WSServer: WSServerConfig{Port: 8081, RPCPort: 9091},
		Kafka:    KafkaConfig{Brokers: []string{"kafka:19092"}, TopicChat: "chat_messages", ConsumerGroup: "im_push_group", TopicAgentTrigger: "private_agent_triggers"},
		TaskNotifications: TaskNotificationsConfig{
			Push: TaskNotificationPushConfig{Enabled: true, Topic: "task_notifications", ConsumerGroup: "task_notification_push",
				WSDNSName: "ws.go-im.internal", Routes: map[string]string{"im-ws:9091": "https://im-ws:9443"},
				TLS: NotificationTLSConfig{CertFile: "push.pem", KeyFile: "push.key", CAFile: "ca.pem"}},
			WS: TaskNotificationWSConfig{Enabled: true, ListenAddr: ":9443", PushDNSName: "push.go-im.internal", UserRPCAddr: "im-user:9000",
				TLS: NotificationTLSConfig{CertFile: "ws.pem", KeyFile: "ws.key", CAFile: "ca.pem"}},
		},
	}
}

func TestTaskNotificationConfigurationDefaultsOffAndRolesRemainIndependent(t *testing.T) {
	for _, role := range []string{"Push", "WS"} {
		t.Run(role, func(t *testing.T) {
			resetNotificationConfigForTest(t)
			validate := ValidateTaskNotificationPush
			if role == "WS" {
				validate = ValidateTaskNotificationWS
			}
			if err := validate(&Config{}); err != nil {
				t.Fatalf("disabled role required addresses or certificates: %v", err)
			}
			if err := validate(nil); err == nil {
				t.Fatal("missing configuration object accepted")
			}
			cfg := validNotificationConfigForTest()
			if role == "Push" {
				cfg.TaskNotifications.WS = TaskNotificationWSConfig{Enabled: true}
			} else {
				cfg.TaskNotifications.Push = TaskNotificationPushConfig{Enabled: true}
				cfg.Kafka = KafkaConfig{}
			}
			if err := validate(cfg); err != nil {
				t.Fatalf("valid role rejected because the other process is incomplete: %v", err)
			}
			if role == "Push" {
				cfg.TaskNotifications.Push = TaskNotificationPushConfig{Enabled: false, Topic: "bad/private", WSDNSName: "*", Routes: map[string]string{"bad": "http://bad"}}
			} else {
				cfg.TaskNotifications.WS = TaskNotificationWSConfig{Enabled: false, ListenAddr: "bad", PushDNSName: "*", UserRPCAddr: "bad"}
			}
			if err := validate(cfg); err != nil {
				t.Fatalf("disabled role validated unused values: %v", err)
			}
		})
	}
}

func TestTaskNotificationPushRejectsIncompleteOrMixedStreamConfiguration(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"missing topic", func(c *Config) { c.TaskNotifications.Push.Topic = "" }},
		{"chat topic", func(c *Config) { c.TaskNotifications.Push.Topic = c.Kafka.TopicChat }},
		{"configured Agent topic", func(c *Config) { c.TaskNotifications.Push.Topic = c.Kafka.TopicAgentTrigger }},
		{"default Agent topic", func(c *Config) { c.TaskNotifications.Push.Topic = "agent_task_triggers" }},
		{"missing chat topic", func(c *Config) { c.Kafka.TopicChat = "" }},
		{"invalid topic", func(c *Config) { c.TaskNotifications.Push.Topic = "task/notifications" }},
		{"special topic", func(c *Config) { c.TaskNotifications.Push.Topic = ".." }},
		{"oversized topic", func(c *Config) { c.TaskNotifications.Push.Topic = strings.Repeat("a", 250) }},
		{"missing group", func(c *Config) { c.TaskNotifications.Push.ConsumerGroup = "" }},
		{"chat consumer group", func(c *Config) { c.TaskNotifications.Push.ConsumerGroup = c.Kafka.ConsumerGroup }},
		{"invalid group", func(c *Config) { c.TaskNotifications.Push.ConsumerGroup = "group with spaces" }},
		{"missing brokers", func(c *Config) { c.Kafka.Brokers = nil }},
		{"bad later broker", func(c *Config) { c.Kafka.Brokers = []string{"kafka:19092", "bad"} }},
		{"missing routes", func(c *Config) { c.TaskNotifications.Push.Routes = nil }},
		{"missing certificate", func(c *Config) { c.TaskNotifications.Push.TLS.CertFile = "" }},
		{"missing key", func(c *Config) { c.TaskNotifications.Push.TLS.KeyFile = "" }},
		{"missing CA", func(c *Config) { c.TaskNotifications.Push.TLS.CAFile = "" }},
		{"padded certificate path", func(c *Config) { c.TaskNotifications.Push.TLS.CertFile = " push.pem " }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetNotificationConfigForTest(t)
			cfg := validNotificationConfigForTest()
			tc.mutate(cfg)
			if err := ValidateTaskNotificationPush(cfg); err == nil {
				t.Fatal("enabled Push accepted invalid configuration")
			}
		})
	}
}

func TestTaskNotificationPushRequiresExplicitBrokerAndControlledHTTPSRoutes(t *testing.T) {
	for _, address := range []string{"", "kafka", ":19092", "http://kafka:19092", "kafka:0", "kafka:65536", "kafka:019092", "kafka:+9092",
		"kafka:9092 ", "bad_host:9092", "-kafka:9092", "kafka..local:9092", "[::1]", "kafka:9092/path"} {
		for _, role := range []string{"broker", "route key"} {
			t.Run(role+" "+address, func(t *testing.T) {
				resetNotificationConfigForTest(t)
				cfg := validNotificationConfigForTest()
				if role == "broker" {
					cfg.Kafka.Brokers = []string{address}
				} else {
					cfg.TaskNotifications.Push.Routes = map[string]string{address: "https://im-ws:9443"}
				}
				if err := ValidateTaskNotificationPush(cfg); err == nil {
					t.Fatal("uncontrolled node address accepted")
				}
			})
		}
	}
	for _, origin := range []string{"", "http://im-ws:9443", "https://im-ws", "https://im-ws:0", "https://im-ws:65536",
		"https://user:password@im-ws:9443", "https://im-ws:9443/", "https://im-ws:9443/internal/task-notifications",
		"https://im-ws:9443?", "https://im-ws:9443?target=other", "https://im-ws:9443#fragment", "https:im-ws:9443",
		"https://bad_host:9443", "https://im-ws:09443", "https://im-ws:9443/%2F", "//im-ws:9443"} {
		t.Run("origin "+origin, func(t *testing.T) {
			resetNotificationConfigForTest(t)
			cfg := validNotificationConfigForTest()
			cfg.TaskNotifications.Push.Routes = map[string]string{"im-ws:9091": origin}
			if err := ValidateTaskNotificationPush(cfg); err == nil {
				t.Fatal("HTTPS origin accepted redirect, credentials or arbitrary URL fields")
			}
		})
	}
	// The online-route node and certificate identity are separately configured.
	t.Run("IPv6 and non-DNS certificate service node", func(t *testing.T) {
		resetNotificationConfigForTest(t)
		cfg := validNotificationConfigForTest()
		cfg.Kafka.Brokers = []string{"[::1]:9092", "127.0.0.1:9093"}
		cfg.TaskNotifications.Push.Routes = map[string]string{"[::1]:9091": "https://[::1]:9443", "127.0.0.1:9091": "https://127.0.0.1:9443"}
		if err := ValidateTaskNotificationPush(cfg); err != nil {
			t.Fatal(err)
		}
	})
}

func TestTaskNotificationRolesRequireExactDNSServiceName(t *testing.T) {
	for _, name := range []string{"", " ", "*.go-im.internal", "ws.go-im.internal:9443", "https://ws.go-im.internal", "bad_host", "127.0.0.1", "::1", "name..internal", "-name.internal", "name-.internal", " name.internal"} {
		t.Run(name, func(t *testing.T) {
			resetNotificationConfigForTest(t)
			cfg := validNotificationConfigForTest()
			cfg.TaskNotifications.Push.WSDNSName = name
			cfg.TaskNotifications.WS.PushDNSName = name
			if err := ValidateTaskNotificationPush(cfg); err == nil {
				t.Fatal("Push accepted ambiguous WS certificate identity")
			}
			if err := ValidateTaskNotificationWS(cfg); err == nil {
				t.Fatal("WS accepted ambiguous Push certificate identity")
			}
		})
	}
}

func TestTaskNotificationWSRejectsListenConflictAndIncompleteDependencies(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"missing listen", func(c *Config) { c.TaskNotifications.WS.ListenAddr = "" }},
		{"external WS port", func(c *Config) { c.TaskNotifications.WS.ListenAddr = ":8081" }},
		{"old internal RPC port", func(c *Config) { c.TaskNotifications.WS.ListenAddr = "127.0.0.1:9091" }},
		{"zero port", func(c *Config) { c.TaskNotifications.WS.ListenAddr = ":0" }},
		{"oversized port", func(c *Config) { c.TaskNotifications.WS.ListenAddr = ":65536" }},
		{"noncanonical port", func(c *Config) { c.TaskNotifications.WS.ListenAddr = ":09443" }},
		{"invalid listen host", func(c *Config) { c.TaskNotifications.WS.ListenAddr = "bad_host:9443" }},
		{"missing User", func(c *Config) { c.TaskNotifications.WS.UserRPCAddr = "" }},
		{"User wildcard host", func(c *Config) { c.TaskNotifications.WS.UserRPCAddr = ":9000" }},
		{"User URL", func(c *Config) { c.TaskNotifications.WS.UserRPCAddr = "http://im-user:9000" }},
		{"User invalid port", func(c *Config) { c.TaskNotifications.WS.UserRPCAddr = "im-user:65536" }},
		{"missing certificate", func(c *Config) { c.TaskNotifications.WS.TLS.CertFile = "" }},
		{"missing key", func(c *Config) { c.TaskNotifications.WS.TLS.KeyFile = "" }},
		{"missing CA", func(c *Config) { c.TaskNotifications.WS.TLS.CAFile = "" }},
		{"padded key path", func(c *Config) { c.TaskNotifications.WS.TLS.KeyFile = " ws.key" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetNotificationConfigForTest(t)
			cfg := validNotificationConfigForTest()
			tc.mutate(cfg)
			if err := ValidateTaskNotificationWS(cfg); err == nil {
				t.Fatal("enabled WS accepted invalid configuration")
			}
		})
	}
	for _, address := range []string{":9443", "0.0.0.0:9443", "127.0.0.1:9443", "[::1]:9443"} {
		t.Run("allowed listen "+address, func(t *testing.T) {
			resetNotificationConfigForTest(t)
			cfg := validNotificationConfigForTest()
			cfg.TaskNotifications.WS.ListenAddr = address
			if err := ValidateTaskNotificationWS(cfg); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLoadTaskNotificationYAMLKeepsRoleFieldsRoutesAndDefaultOff(t *testing.T) {
	const complete = `ws_server:
  port: 8081
  rpc_port: 9091
kafka:
  brokers: ["kafka:19092", "[::1]:9092"]
  topic_chat: chat_messages
  topic_agent_trigger: private_agent_triggers
  consumer_group: im_push_group
task_notifications:
  push:
    enabled: true
    topic: task_notifications
    consumer_group: task_notification_push
    ws_dns_name: ws.go-im.internal
    routes:
      "im-ws:9091": "https://im-ws:9443"
      "[::1]:9091": "https://[::1]:9443"
    tls:
      cert_file: push.pem
      key_file: push.key
      ca_file: ca.pem
  ws:
    enabled: true
    listen_addr: ":9443"
    push_dns_name: push.go-im.internal
    user_rpc_addr: im-user:9000
    tls:
      cert_file: ws.pem
      key_file: ws.key
      ca_file: ca.pem
`
	for _, tc := range []struct {
		name string
		yaml string
		full bool
	}{
		{"both roles", complete, true},
		{"legacy YAML without notification block", "app:\n  env: test\n", false},
		{"empty role sections", "task_notifications:\n  push: {}\n  ws: {}\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resetNotificationConfigForTest(t)
			configPath := filepath.Join(t.TempDir(), "notification.yaml")
			if err := os.WriteFile(configPath, []byte(tc.yaml), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if cfg != GlobalConfig {
				t.Fatal("Load did not publish the same configuration object")
			}
			if tc.full {
				want := validNotificationConfigForTest()
				want.Kafka.Brokers = []string{"kafka:19092", "[::1]:9092"}
				want.TaskNotifications.Push.Routes["[::1]:9091"] = "https://[::1]:9443"
				if !reflect.DeepEqual(cfg.TaskNotifications, want.TaskNotifications) || !reflect.DeepEqual(cfg.Kafka, want.Kafka) || cfg.WSServer != want.WSServer {
					t.Fatalf("YAML lost notification roles, routes or shared config:\ngot %+v\nwant %+v", cfg, want)
				}
			} else if !reflect.DeepEqual(cfg.TaskNotifications, TaskNotificationsConfig{}) {
				t.Fatalf("missing block must leave both roles disabled: %+v", cfg.TaskNotifications)
			}
			if err := ValidateTaskNotificationPush(cfg); err != nil {
				t.Fatal(err)
			}
			if err := ValidateTaskNotificationWS(cfg); err != nil {
				t.Fatal(err)
			}
		})
	}
}
