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
    totalProcessingTimeNs int64

    // Rate limiter (initialized in main after env config)
    rateLimiter *rate.Limiter

    // Per-IP rate limiters
    ipLimiters = struct {
        sync.Mutex
        m map[string]*rate.Limiter
    }{m: make(map[string]*rate.Limiter)}

    // API keys set
    apiKeys = map[string]struct{}{}

    // Download trackers and deletion scheduling guards
    downloadTrackers = struct {
        sync.Mutex
        inProgress map[string]int
        scheduled  map[string]bool
    }{inProgress: make(map[string]int), scheduled: make(map[string]bool)}

    // Job cancellation registry
    jobCancels = struct {
        sync.Mutex
        m map[string]context.CancelFunc
    }{m: make(map[string]context.CancelFunc)}

    // Admin live logs ring buffer and subscribers
    adminLogs = struct {
        sync.Mutex
        lines []string
        subs  map[chan string]struct{}
    }{lines: make([]string, 0, 500), subs: make(map[chan string]struct{})}

    // Redis client
    redisClient *redis.Client

    // Server start time
    serverStartTime = time.Now()

    // Context for graceful shutdown
    ctx, cancel = context.WithCancel(context.Background())
)

// Append a log line to ring buffer and broadcast to subscribers
func appendAdminLog(line string) {
    adminLogs.Lock()
    if len(adminLogs.lines) >= 500 {
        copy(adminLogs.lines, adminLogs.lines[1:])
        adminLogs.lines[len(adminLogs.lines)-1] = line
    } else {
        adminLogs.lines = append(adminLogs.lines, line)
    }
    for ch := range adminLogs.subs {
        select { case ch <- line: default: }
    }
    adminLogs.Unlock()
}

// Writer to hook standard logger
type adminLogWriter struct{}

func (w adminLogWriter) Write(p []byte) (int, error) {
    appendAdminLog(string(p))
    return len(p), nil
}

func subscribeAdminLogs() chan string {
    ch := make(chan string, 100)
    adminLogs.Lock()
    adminLogs.subs[ch] = struct{}{}
    adminLogs.Unlock()
    return ch
}

func unsubscribeAdminLogs(ch chan string) {
    adminLogs.Lock()
    delete(adminLogs.subs, ch)
    close(ch)
    adminLogs.Unlock()
}

// Waiters notified when a job reaches a terminal state (completed or failed).
var jobWaiters = struct {
    sync.Mutex
    m map[string][]chan *ConversionJob
}{m: make(map[string][]chan *ConversionJob)}
