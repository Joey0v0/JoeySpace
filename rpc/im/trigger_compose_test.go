package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type triggerComposePushTemplate struct {
	Kafka struct {
		AgentTriggerEnabled bool     `yaml:"agent_trigger_enabled"`
		TopicAgentTrigger   string   `yaml:"topic_agent_trigger"`
		TopicChat           string   `yaml:"topic_chat"`
		ConsumerGroup       string   `yaml:"consumer_group"`
		Brokers             []string `yaml:"brokers"`
	} `yaml:"kafka"`
}

type triggerComposeService struct {
	Environment map[string]string `yaml:"environment"`
	Ports       []any             `yaml:"ports"`
	NetworkMode string            `yaml:"network_mode"`
	Volumes     []struct {
		Type     string `yaml:"type"`
		Source   string `yaml:"source"`
		Target   string `yaml:"target"`
		ReadOnly bool   `yaml:"read_only"`
		Bind     struct {
			CreateHostPath *bool `yaml:"create_host_path"`
		} `yaml:"bind"`
	} `yaml:"volumes"`
	DependsOn map[string]struct {
		Condition string `yaml:"condition"`
	} `yaml:"depends_on"`
}

func readTriggerComposeYAML(t *testing.T, file string, target any) {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, target); err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
}

func triggerComposeOverlay(t *testing.T) map[string]triggerComposeService {
	t.Helper()
	var overlay struct {
		Services map[string]triggerComposeService `yaml:"services"`
	}
	readTriggerComposeYAML(t, "../../deploy/docker-compose.trigger.yaml", &overlay)
	if len(overlay.Services) != 3 {
		t.Fatalf("trigger overlay must configure only User, IM and Agent: %d services", len(overlay.Services))
	}
	for _, name := range []string{"user-rpc", "im-rpc", "agent-rpc"} {
		service, ok := overlay.Services[name]
		if !ok || len(service.Environment) == 0 || len(service.Ports) != 0 || service.NetworkMode != "" {
			t.Fatalf("missing internal-only trigger service %s", name)
		}
	}
	return overlay.Services
}

// Read environment names from the startup sources so a misspelled deployment
// key cannot pass by agreeing only with a second handwritten YAML fixture.
func triggerComposeStartupKeys(t *testing.T, files ...string) map[string]bool {
	t.Helper()
	keys := make(map[string]bool)
	variable := regexp.MustCompile(`^(USER_TRIGGER_|IM_TRIGGER_|AGENT_TRIGGER_|AGENT_IM_TRIGGER_)[A-Z0-9_]+$`)
	for _, file := range files {
		parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, ok := node.(*ast.BasicLit)
			if ok && literal.Kind == token.STRING {
				value, err := strconv.Unquote(literal.Value)
				if err == nil && variable.MatchString(value) {
					keys[value] = true
				}
			}
			return true
		})
	}
	return keys
}

func TestTriggerComposeOverlayMatchesStartupAndServiceRoutes(t *testing.T) {
	services := triggerComposeOverlay(t)
	files := map[string][]string{
		"user-rpc":  {"../user/trigger_listener.go"},
		"im-rpc":    {"trigger_listener.go", "trigger_team_client.go"},
		"agent-rpc": {"../../cmd/agent/trigger_inbox.go", "../../cmd/agent/trigger_worker.go", "../agent/trigger_client.go"},
	}
	for service, sources := range files {
		keys := triggerComposeStartupKeys(t, sources...)
		env := services[service].Environment
		if len(env) != len(keys) {
			t.Fatalf("%s deployment/startup environment counts differ: %d/%d", service, len(env), len(keys))
		}
		for key := range keys {
			if env[key] == "" || strings.TrimSpace(env[key]) != env[key] {
				t.Fatalf("%s startup variable %s missing or blank", service, key)
			}
		}
	}
	imEnv, userEnv, agentEnv := services["im-rpc"].Environment, services["user-rpc"].Environment, services["agent-rpc"].Environment
	im, err := loadIMTriggerConfig(func(key string) string { return imEnv[key] })
	if err != nil || im.disabled() || im.ListenOn != "0.0.0.0:9006" {
		t.Fatalf("IM startup cannot consume trigger overlay: %+v %v", im, err)
	}
	if err := validateIMTriggerStartup(im, "0.0.0.0:9002", "0.0.0.0:9005"); err != nil {
		t.Fatal(err)
	}
	teams, err := loadIMTriggerTeamConfig(im, func(key string) string { return imEnv[key] })
	if err != nil {
		t.Fatal(err)
	}
	_, imPort, _ := net.SplitHostPort(im.ListenOn)
	_, userPort, err := net.SplitHostPort(userEnv["USER_TRIGGER_LISTEN_ON"])
	if err != nil || userEnv["USER_TRIGGER_LISTEN_ON"] != "0.0.0.0:9007" ||
		teams.Addr != net.JoinHostPort("user-rpc", userPort) || agentEnv["AGENT_IM_TRIGGER_ADDR"] != net.JoinHostPort("im-rpc", imPort) {
		t.Fatal("dedicated clients do not point to the corresponding internal listeners")
	}
	if im.AgentDNSName != "agent.go-im.internal" || userEnv["USER_TRIGGER_IM_DNS_NAME"] != "im.go-im.internal" ||
		agentEnv["AGENT_IM_TRIGGER_SERVER_NAME"] != userEnv["USER_TRIGGER_IM_DNS_NAME"] || teams.ServerDNSName != "user.go-im.internal" {
		t.Fatal("trigger service identities differ from their certificate provisioning contract")
	}
	var pushConfig triggerComposePushTemplate
	readTriggerComposeYAML(t, "../../deploy/docker-config.yaml", &pushConfig)
	if agentEnv["AGENT_TRIGGER_INBOX_ENABLED"] != "true" || agentEnv["AGENT_TRIGGER_WORKER_ENABLED"] != "true" ||
		agentEnv["AGENT_TRIGGER_KAFKA_TOPIC"] != pushConfig.Kafka.TopicAgentTrigger || pushConfig.Kafka.TopicAgentTrigger == "" ||
		pushConfig.Kafka.TopicAgentTrigger == pushConfig.Kafka.TopicChat ||
		agentEnv["AGENT_TRIGGER_KAFKA_BROKERS"] != strings.Join(pushConfig.Kafka.Brokers, ",") ||
		agentEnv["AGENT_TRIGGER_KAFKA_GROUP"] != "agent_task_trigger_group" || agentEnv["AGENT_TRIGGER_KAFKA_GROUP"] == pushConfig.Kafka.ConsumerGroup ||
		services["agent-rpc"].DependsOn["kafka"].Condition != "service_healthy" {
		t.Fatal("trigger intake and Push publisher Kafka configuration or startup ordering differ")
	}
}

func TestTriggerComposeCertificatesArePrivateIndependentExistingBinds(t *testing.T) {
	services := triggerComposeOverlay(t)
	data, err := os.ReadFile("../../deploy/.env.example")
	if err != nil {
		t.Fatal(err)
	}
	example := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok {
			if _, duplicate := example[key]; duplicate {
				t.Fatalf("duplicate variable in public environment example: %s", key)
			}
			example[key] = value
		}
	}
	type certificateMount struct{ directory, prefix string }
	wanted := map[string]map[string]certificateMount{
		"user-rpc": {"/app/certs/user-trigger": {"USER_TRIGGER_CERT_DIR", "USER_TRIGGER_TLS_"}},
		"im-rpc": {
			"/app/certs/im-trigger":      {"IM_TRIGGER_CERT_DIR", "IM_TRIGGER_TLS_"},
			"/app/certs/im-trigger-user": {"IM_TRIGGER_USER_CERT_DIR", "IM_TRIGGER_USER_TLS_"},
		},
		"agent-rpc": {"/app/certs/agent-trigger": {"AGENT_TRIGGER_CERT_DIR", "AGENT_IM_TRIGGER_TLS_"}},
	}
	seen := make(map[string]bool)
	for name, mounts := range wanted {
		service := services[name]
		if len(service.Volumes) != len(mounts) {
			t.Fatalf("%s has unexpected certificate mount count", name)
		}
		for _, volume := range service.Volumes {
			mount, ok := mounts[volume.Target]
			if !ok || seen[mount.directory] || volume.Type != "bind" || !volume.ReadOnly ||
				volume.Bind.CreateHostPath == nil || *volume.Bind.CreateHostPath ||
				!strings.HasPrefix(volume.Source, "${"+mount.directory+":?") || !strings.HasSuffix(volume.Source, "}") {
				t.Fatalf("%s certificate mount permits missing/private/shared directory: %+v", name, volume)
			}
			seen[mount.directory] = true
			if value, ok := example[mount.directory]; !ok || value != "" {
				t.Fatalf("%s must be an empty private-directory example", mount.directory)
			}
			for suffix, file := range map[string]string{"CERT_FILE": "cert.pem", "KEY_FILE": "key.pem", "CA_FILE": "ca.pem"} {
				if service.Environment[mount.prefix+suffix] != path.Join(volume.Target, file) {
					t.Fatalf("%s certificate file %s escapes its own mount", name, mount.prefix+suffix)
				}
			}
		}
	}
	if len(seen) != 4 {
		t.Fatal("trigger roles must use four independent private directory variables")
	}
}

func TestTriggerComposeBaseRemainsOptInAndPushTemplateDisabled(t *testing.T) {
	var base struct {
		Services map[string]struct {
			Environment map[string]string `yaml:"environment"`
			Profiles    []string          `yaml:"profiles"`
		} `yaml:"services"`
	}
	readTriggerComposeYAML(t, "../../deploy/docker-compose.yaml", &base)
	for name, service := range base.Services {
		for key := range service.Environment {
			if strings.HasPrefix(key, "USER_TRIGGER_") || strings.HasPrefix(key, "IM_TRIGGER_") ||
				strings.HasPrefix(key, "AGENT_TRIGGER_") || strings.HasPrefix(key, "AGENT_IM_TRIGGER_") {
				t.Fatalf("base service %s configures opt-in trigger variable %s", name, key)
			}
		}
	}
	profiles := base.Services["agent-rpc"].Profiles
	if len(profiles) != 1 || profiles[0] != "agent" {
		t.Fatal("base Agent must retain its explicit agent profile")
	}
	var pushConfig triggerComposePushTemplate
	readTriggerComposeYAML(t, "../../deploy/docker-config.yaml", &pushConfig)
	if pushConfig.Kafka.AgentTriggerEnabled {
		t.Fatal("public Push template enabled trigger intake without private deployment approval")
	}
}
