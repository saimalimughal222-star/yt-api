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

	// Job Expiration (as duration)
	JobExpiration = 24 * time.Hour

	// Health Check
	HealthCheckInterval = 30 * time.Second

	// Fast-path response: wait briefly for quick jobs
	FastPathWait = 8 * time.Second

	// Security & Abuse
	AllowedOrigins     = "*" // comma-separated
	RequireAPIKey      = false
	APIKeysCSV         = ""   // comma-separated list
	PerIPRPS           = 10
	PerIPBurst         = 20
	MaxURLLength       = 2048

	// Retry/backoff
	BackoffBaseSeconds = 5
	BackoffMaxSeconds  = 60

	// Admin UI credentials
	AdminUser = ""
	AdminPass = ""

	// Max processing time per job
	JobMaxProcessing = 30 * time.Minute
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

	// Prefer JOB_EXPIRATION (duration like "24h"); fallback to JOB_EXPIRATION_HOURS (int hours)
	JobExpiration = envDuration("JOB_EXPIRATION", JobExpiration)
	if os.Getenv("JOB_EXPIRATION") == "" {
		defHours := int(JobExpiration / time.Hour)
		h := envInt("JOB_EXPIRATION_HOURS", defHours)
		JobExpiration = time.Duration(h) * time.Hour
	}

	HealthCheckInterval = envDuration("HEALTH_CHECK_INTERVAL", HealthCheckInterval)
	FastPathWait = envDuration("FAST_PATH_WAIT", FastPathWait)

	AllowedOrigins = envString("ALLOWED_ORIGINS", AllowedOrigins)
	RequireAPIKey = envString("REQUIRE_API_KEY", "false") == "true"
	APIKeysCSV = envString("API_KEYS", APIKeysCSV)
	PerIPRPS = envInt("PER_IP_RPS", PerIPRPS)
	PerIPBurst = envInt("PER_IP_BURST", PerIPBurst)
	MaxURLLength = envInt("MAX_URL_LENGTH", MaxURLLength)

	BackoffBaseSeconds = envInt("BACKOFF_BASE_SECONDS", BackoffBaseSeconds)
	BackoffMaxSeconds = envInt("BACKOFF_MAX_SECONDS", BackoffMaxSeconds)

	AdminUser = envString("ADMIN_USER", AdminUser)
	AdminPass = envString("ADMIN_PASS", AdminPass)

	JobMaxProcessing = envDuration("JOB_MAX_PROCESSING", JobMaxProcessing)
}
