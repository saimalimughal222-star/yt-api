package main

import (
    "context"
    "sync"
    "time"

    redis "github.com/redis/go-redis/v9"
    "golang.org/x/time/rate"
)

var (
    jobQueue chan *ConversionJob

    jobStore = struct {
        sync.RWMutex
        jobs map[string]*ConversionJob
    }{jobs: make(map[string]*ConversionJob)}

    // Metrics
    activeJobs    int64
    queuedJobs    int64
    completedJobs int64
    failedJobs    int64

    // Rate limiter
    rateLimiter = rate.NewLimiter(rate.Limit(RequestsPerSecond), BurstSize)

    // Redis client
    redisClient *redis.Client

    // Server start time
    serverStartTime = time.Now()

    // Context for graceful shutdown
    ctx, cancel = context.WithCancel(context.Background())
)

// Waiters notified when a job reaches a terminal state (completed or failed).
var jobWaiters = struct {
    sync.Mutex
    m map[string][]chan *ConversionJob
}{m: make(map[string][]chan *ConversionJob)}
