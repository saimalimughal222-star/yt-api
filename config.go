package main

import "time"

// Centralized configuration values
const (
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
