package main

import (
    "fmt"
    "log"
    "os"
    "time"
)

func updateJobStatus(job *ConversionJob, status JobStatus, errMsg string) {
    job.Status = status
    job.Error = errMsg
    if status == StatusFailed {
        job.CompletedAt = time.Now()
    }
    jobStore.Lock()
    jobStore.jobs[job.ID] = job
    jobStore.Unlock()
    saveJobToRedis(job)
    log.Printf("Job %s status updated to %s: %s\n", job.ID, status, errMsg)
}

func handleJobFailure(job *ConversionJob, err error, stage string) {
    job.Retries++
    if job.Retries <= job.MaxRetries {
        log.Printf("Job %s (%s): %s. Retrying (%d/%d)...\n", job.ID, job.URL, err.Error(), job.Retries, job.MaxRetries)
        time.Sleep(5 * time.Second)
        select {
        case jobQueue <- job:
        default:
            updateJobStatus(job, StatusFailed, fmt.Sprintf("%s: %v. Max retries exceeded and queue full.", stage, err))
            notifyJobCompletion(job)
        }
    } else {
        updateJobStatus(job, StatusFailed, fmt.Sprintf("%s: %v. Max retries (%d) exceeded.", stage, err, job.MaxRetries))
        notifyJobCompletion(job)
    }
}

func findJobByURL(url string) *ConversionJob {
    jobStore.RLock()
    defer jobStore.RUnlock()
    for _, job := range jobStore.jobs {
        if job.URL == url && job.Status == StatusCompleted {
            return job
        }
    }
    return nil
}

func startHealthCheck() {
    ticker := time.NewTicker(HealthCheckInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            // Log current stats
        case <-ctx.Done():
            return
        }
    }
}

func startJobCleanup() {
    ticker := time.NewTicker(1 * time.Hour)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            cleanupOldJobs()
        case <-ctx.Done():
            return
        }
    }
}

func cleanupOldJobs() {
    cutoff := time.Now().Add(-JobExpirationHours * time.Hour)
    jobStore.Lock()
    for id, job := range jobStore.jobs {
        if job.CreatedAt.Before(cutoff) {
            if job.FilePath != "" {
                _ = os.Remove(job.FilePath)
            }
            delete(jobStore.jobs, id)
        }
    }
    jobStore.Unlock()
}
