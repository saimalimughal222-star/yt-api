package main

import (
    "fmt"
    "log"
    "net/http"

    "golang.org/x/time/rate"
)

func main() {
    // Load configuration from environment
    InitConfigFromEnv()

    // Initialize Redis
    initRedis()

    // Initialize job queue
    jobQueue = make(chan *ConversionJob, JobQueueCapacity)

    // Initialize rate limiter
    rateLimiter = rate.NewLimiter(rate.Limit(RequestsPerSecond), BurstSize)

    // Start worker pool
    for i := 0; i < WorkerPoolSize; i++ {
        go startWorker(i)
    }

    // Background routines
    go startHealthCheck()
    go startJobCleanup()

    // Setup HTTP routes with middleware
    mux := http.NewServeMux()
    mux.HandleFunc("/extract", rateLimitMiddleware(apiKeyMiddleware(handleExtract)))
    mux.HandleFunc("/status/", rateLimitMiddleware(apiKeyMiddleware(handleStatus)))
    mux.HandleFunc("/download/", rateLimitMiddleware(apiKeyMiddleware(handleDownload)))
    mux.HandleFunc("/health", handleHealth)
    mux.HandleFunc("/metrics", handleMetrics)
    mux.HandleFunc("/stats", handleStats)
    mux.HandleFunc("/delete/", handleDelete)
    mux.HandleFunc("/cancel/", rateLimitMiddleware(apiKeyMiddleware(handleCancel)))
    mux.HandleFunc("/docs", handleDocs)
    mux.HandleFunc("/docs/frontend", handleDocsFrontend)
    mux.HandleFunc("/admin", basicAuthMiddleware(handleAdmin))
    mux.HandleFunc("/admin/data", basicAuthMiddleware(handleAdminData))
    mux.HandleFunc("/admin/cancel/", basicAuthMiddleware(handleAdminCancel))
    mux.HandleFunc("/admin/delete/", basicAuthMiddleware(handleAdminDelete))
    mux.HandleFunc("/admin/config", basicAuthMiddleware(handleAdminConfig))

    // Graceful shutdown setup
    setupGracefulShutdown()

    fmt.Printf("🚀 High-Traffic Server running on http://localhost:8080 with %d workers\n", WorkerPoolSize)
    fmt.Printf("📊 Rate Limit: %d req/s (burst: %d)\n", RequestsPerSecond, BurstSize)
    fmt.Printf("💾 Redis: %s\n", RedisAddr)

    log.Fatal(http.ListenAndServe(":8080", mux))
}
