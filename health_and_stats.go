package main

import (
    "encoding/json"
    "net/http"
    "path/filepath"
    "sync/atomic"
    "time"
    "os"
)

func handleHealth(w http.ResponseWriter, r *http.Request) {
    enableCORS(w)
    status := "healthy"
    // Consider both active and queued jobs as load indicator
    if atomic.LoadInt64(&activeJobs) >= int64(WorkerPoolSize) || atomic.LoadInt64(&queuedJobs) > int64(JobQueueCapacity/2) {
        status = "overloaded"
    }
    health := HealthStatus{
        Status:        status,
        ActiveJobs:    atomic.LoadInt64(&activeJobs),
        QueuedJobs:    atomic.LoadInt64(&queuedJobs),
        CompletedJobs: atomic.LoadInt64(&completedJobs),
        FailedJobs:    atomic.LoadInt64(&failedJobs),
        Workers:       WorkerPoolSize,
        Uptime:        time.Since(serverStartTime).String(),
        MemoryUsage:   getMemoryUsage(),
    }
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(health)
}

func handleMetrics(w http.ResponseWriter, r *http.Request) {
    enableCORS(w)
    metrics := map[string]interface{}{
        "active_jobs":    atomic.LoadInt64(&activeJobs),
        "queued_jobs":    atomic.LoadInt64(&queuedJobs),
        "completed_jobs": atomic.LoadInt64(&completedJobs),
        "failed_jobs":    atomic.LoadInt64(&failedJobs),
        "workers":        WorkerPoolSize,
        "queue_capacity": JobQueueCapacity,
        "rate_limit":     RequestsPerSecond,
        "uptime_seconds": time.Since(serverStartTime).Seconds(),
        "success_rate":   calculateSuccessRate(),
        "avg_processing_s": getAvgProcessingTime(),
    }
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(metrics)
}

func handleStats(w http.ResponseWriter, r *http.Request) {
    enableCORS(w)
    jobStore.RLock()
    totalJobs := len(jobStore.jobs)
    jobStore.RUnlock()

    stats := map[string]interface{}{
        "total_jobs":           totalJobs,
        "active_jobs":          atomic.LoadInt64(&activeJobs),
        "queued_jobs":          atomic.LoadInt64(&queuedJobs),
        "completed_jobs":       atomic.LoadInt64(&completedJobs),
        "failed_jobs":          atomic.LoadInt64(&failedJobs),
        "success_rate":         calculateSuccessRate(),
        "avg_processing_time":  getAvgProcessingTime(),
    }
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(stats)
}

// DELETE /delete/{job_id}
func handleDelete(w http.ResponseWriter, r *http.Request) {
    enableCORS(w)
    if r.Method == http.MethodOptions {
        w.WriteHeader(http.StatusOK)
        return
    }
    if r.Method != http.MethodDelete {
        http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
        return
    }
    jobID := filepath.Base(r.URL.Path)
    if jobID == "" {
        http.Error(w, "Missing job ID", http.StatusBadRequest)
        return
    }
    var job *ConversionJob
    jobStore.RLock()
    j, exists := jobStore.jobs[jobID]
    jobStore.RUnlock()
    if exists {
        job = j
    } else {
        if rj, err := getJobFromRedis(jobID); err == nil && rj != nil {
            job = rj
        }
    }
    // Attempt to remove file by known path or default path
    if job != nil && job.FilePath != "" {
        _ = os.Remove(job.FilePath)
    } else {
        _ = os.Remove(filepath.Join("downloads", jobID+".mp3"))
    }
    // Remove in-memory
    jobStore.Lock()
    delete(jobStore.jobs, jobID)
    jobStore.Unlock()
    // Remove Redis job and URL mapping
    deleteJobFromRedis(jobID)
    if job != nil && job.URL != "" {
        removeURLMapping(job.URL)
    }
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"deleted": jobID})
}
