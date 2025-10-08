package main

import (
	"os"
	"strconv"
	"time"
)

// Centralized configuration values (env-overridable)
var (
	// Worker Configuration
	WorkerPoolSize   = 20
	MaxJobRetries    = 3
	JobQueueCapacity = 1000

	// Rate Limiting
	RequestsPerSecond = 100
	BurstSize         = 200

	// Redis Configuration
	RedisAddr     = "localhost:6379"
	RedisPassword = ""
	RedisDB       = 0

	// Job Expiration
	JobExpirationHours = 24

	// Health Check
	HealthCheckInterval = 30 * time.Second

	// Fast-path response: wait briefly for quick jobs
	FastPathWait = 8 * time.Second
)

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func envString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		// fallback: seconds as int
		if i, err := strconv.Atoi(v); err == nil {
			return time.Duration(i) * time.Second
		}
	}
	return def
}

// InitConfigFromEnv should be called early in main() before using these values
func InitConfigFromEnv() {
	WorkerPoolSize = envInt("WORKER_POOL_SIZE", WorkerPoolSize)
	MaxJobRetries = envInt("MAX_JOB_RETRIES", MaxJobRetries)
	JobQueueCapacity = envInt("JOB_QUEUE_CAPACITY", JobQueueCapacity)

	RequestsPerSecond = envInt("REQUESTS_PER_SECOND", RequestsPerSecond)
	BurstSize = envInt("BURST_SIZE", BurstSize)

	RedisAddr = envString("REDIS_ADDR", RedisAddr)
	RedisPassword = envString("REDIS_PASSWORD", RedisPassword)
	RedisDB = envInt("REDIS_DB", RedisDB)

	JobExpirationHours = envInt("JOB_EXPIRATION_HOURS", JobExpirationHours)

	HealthCheckInterval = envDuration("HEALTH_CHECK_INTERVAL", HealthCheckInterval)
	FastPathWait = envDuration("FAST_PATH_WAIT", FastPathWait)
}
