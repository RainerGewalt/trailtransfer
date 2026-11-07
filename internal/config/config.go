// Package config loads worker configuration from an optional YAML file with
// TRAILTRANSFER_* environment overrides, applies safe defaults, and derives the
// per-worker MQTT topic names.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Config is the worker's runtime configuration. Secrets for transfer backends
// live in rclone.conf, never here.
type Config struct {
	WorkerID    string `yaml:"worker_id"`
	TopicPrefix string `yaml:"topic_prefix"`

	MQTT struct {
		Host             string `yaml:"host"`
		Port             int    `yaml:"port"`
		Username         string `yaml:"username"`
		Password         string `yaml:"password"`
		KeepAliveSeconds int    `yaml:"keep_alive_seconds"`
	} `yaml:"mqtt"`

	PolicyFile string `yaml:"policy_file"`
	// PolicyID names the policy in evidence records (policy.id). Empty = the
	// base name of PolicyFile.
	PolicyID     string `yaml:"policy_id"`
	RcloneConfig string `yaml:"rclone_config"`
	RcloneBinary string `yaml:"rclone_binary"`

	// EvidenceJournal is the append-only journal path. Empty = in-memory chain
	// only (resets on restart); set a path on a writable/WORM volume for durable,
	// recoverable evidence.
	EvidenceJournal string `yaml:"evidence_journal"`

	// SigningKey is a PEM Ed25519 private key path. When set, every sealed record
	// is signed with it. Empty = unsigned chain.
	SigningKey string `yaml:"signing_key"`

	MaxParallelJobs       int    `yaml:"max_parallel_jobs"`
	HealthIntervalSeconds int    `yaml:"health_interval_seconds"`
	LogLevel              string `yaml:"log_level"`
}

func defaults() *Config {
	c := &Config{
		TopicPrefix:           "trailtransfer",
		RcloneBinary:          "rclone",
		MaxParallelJobs:       2,
		HealthIntervalSeconds: 30,
		LogLevel:              "info",
	}
	c.MQTT.Port = 1883
	c.MQTT.KeepAliveSeconds = 30
	return c
}

// Load reads the optional YAML file at path (skipped if empty), overlays
// TRAILTRANSFER_* environment variables, applies defaults and validates.
func Load(path string) (*Config, error) {
	c := defaults()

	if path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config %q: %w", path, err)
		}
		if err := yaml.Unmarshal(raw, c); err != nil {
			return nil, fmt.Errorf("parse config %q: %w", path, err)
		}
	}

	c.applyEnv()

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) applyEnv() {
	setStr(&c.WorkerID, "TRAILTRANSFER_WORKER_ID")
	setStr(&c.TopicPrefix, "TRAILTRANSFER_TOPIC_PREFIX")
	setStr(&c.MQTT.Host, "TRAILTRANSFER_MQTT_HOST")
	setInt(&c.MQTT.Port, "TRAILTRANSFER_MQTT_PORT")
	setStr(&c.MQTT.Username, "TRAILTRANSFER_MQTT_USERNAME")
	setStr(&c.MQTT.Password, "TRAILTRANSFER_MQTT_PASSWORD")
	setStr(&c.PolicyFile, "TRAILTRANSFER_POLICY_FILE")
	setStr(&c.PolicyID, "TRAILTRANSFER_POLICY_ID")
	setStr(&c.RcloneConfig, "TRAILTRANSFER_RCLONE_CONFIG")
	setStr(&c.RcloneBinary, "TRAILTRANSFER_RCLONE_BINARY")
	setStr(&c.EvidenceJournal, "TRAILTRANSFER_EVIDENCE_JOURNAL")
	setStr(&c.SigningKey, "TRAILTRANSFER_SIGNING_KEY")
	setInt(&c.MaxParallelJobs, "TRAILTRANSFER_MAX_PARALLEL_JOBS")
	setInt(&c.HealthIntervalSeconds, "TRAILTRANSFER_HEALTH_INTERVAL_SECONDS")
	setStr(&c.LogLevel, "TRAILTRANSFER_LOG_LEVEL")
}

func (c *Config) validate() error {
	if c.WorkerID == "" {
		return fmt.Errorf("worker_id is required (TRAILTRANSFER_WORKER_ID)")
	}
	if c.MQTT.Host == "" {
		return fmt.Errorf("mqtt host is required (TRAILTRANSFER_MQTT_HOST)")
	}
	if c.MQTT.Port <= 0 || c.MQTT.Port > 65535 {
		return fmt.Errorf("mqtt port %d is invalid", c.MQTT.Port)
	}
	if c.PolicyFile == "" {
		return fmt.Errorf("policy_file is required — the policy is the security boundary (TRAILTRANSFER_POLICY_FILE)")
	}
	if c.RcloneConfig == "" {
		return fmt.Errorf("rclone_config is required (TRAILTRANSFER_RCLONE_CONFIG)")
	}
	if c.PolicyID == "" {
		c.PolicyID = filepath.Base(c.PolicyFile)
	}
	if c.HealthIntervalSeconds <= 0 {
		c.HealthIntervalSeconds = 30
	}
	return nil
}

// BrokerURL returns the TCP broker address for the MQTT client.
func (c *Config) BrokerURL() string {
	return fmt.Sprintf("tcp://%s:%d", c.MQTT.Host, c.MQTT.Port)
}

func (c *Config) base() string { return c.TopicPrefix + "/" + c.WorkerID }

func (c *Config) CommandTopic() string      { return c.base() + "/commands" }
func (c *Config) HealthTopic() string       { return c.base() + "/health" }
func (c *Config) CapabilitiesTopic() string { return c.base() + "/capabilities" }

func (c *Config) jobTopic(jobID, suffix string) string {
	return c.base() + "/jobs/" + jobID + "/" + suffix
}

func (c *Config) JobStatusTopic(jobID string) string   { return c.jobTopic(jobID, "status") }
func (c *Config) JobProgressTopic(jobID string) string { return c.jobTopic(jobID, "progress") }
func (c *Config) JobResultTopic(jobID string) string   { return c.jobTopic(jobID, "result") }
func (c *Config) JobLogsTopic(jobID string) string     { return c.jobTopic(jobID, "logs") }

func setStr(dst *string, env string) {
	if v, ok := os.LookupEnv(env); ok && v != "" {
		*dst = v
	}
}

func setInt(dst *int, env string) {
	if v, ok := os.LookupEnv(env); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			*dst = n
		}
	}
}
