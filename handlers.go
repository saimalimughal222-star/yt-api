package main

import (
    "encoding/json"
    "fmt"
    "io"
    "net/http"
    "os"
    "path/filepath"
    "sync/atomic"
    "time"

    "github.com/google/uuid"
)

func handleExtract(w http.ResponseWriter, r *http.Request) {
    enableCORS(w)

    if r.Method == http.MethodOptions {
        w.WriteHeader(http.StatusOK)
        return
    }
    if r.Method != http.MethodPost {
        http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
        return
    }

    var req Request
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
        http.Error(w, "Invalid JSON", http.StatusBadRequest)
        return
    }
    if req.URL == "" {
        http.Error(w, "Missing YouTube URL", http.StatusBadRequest)
        return
    }

    existingJob := findJobByURL(req.URL)
    if existingJob != nil && existingJob.Status == StatusCompleted {
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]string{
            "job_id": existingJob.ID,
            "status": string(existingJob.Status),
            "download_url": existingJob.DownloadURL,
            "check_status_endpoint": fmt.Sprintf("http://localhost:8080/status/%s", existingJob.ID),
        })
        return
    }

    jobID := uuid.New().String()
    job := &ConversionJob{
        ID:         jobID,
        URL:        req.URL,
        Status:     StatusPending,
        CreatedAt:  time.Now(),
        MaxRetries: MaxJobRetries,
        Priority:   1,
    }

    jobStore.Lock()
    jobStore.jobs[jobID] = job
    jobStore.Unlock()

    saveJobToRedis(job)
    atomic.AddInt64(&queuedJobs, 1)

    resultCh := registerJobWaiter(jobID)

    select {
    case jobQueue <- job:
        w.Header().Set("Content-Type", "application/json")
        select {
        case doneJob := <-resultCh:
            if doneJob.Status == StatusCompleted {
                json.NewEncoder(w).Encode(map[string]string{
                    "job_id": jobID,
                    "status": string(doneJob.Status),
                    "download_url": doneJob.DownloadURL,
                    "check_status_endpoint": fmt.Sprintf("http://localhost:8080/status/%s", jobID),
                })
            } else {
                json.NewEncoder(w).Encode(map[string]interface{}{
                    "job_id": jobID,
                    "status": string(doneJob.Status),
                    "error": doneJob.Error,
                    "check_status_endpoint": fmt.Sprintf("http://localhost:8080/status/%s", jobID),
                })
            }
        case <-time.After(FastPathWait):
            unregisterJobWaiter(jobID, resultCh)
            json.NewEncoder(w).Encode(map[string]string{
                "job_id": jobID,
                "status": string(job.Status),
                "check_status_endpoint": fmt.Sprintf("http://localhost:8080/status/%s", jobID),
            })
        }
    default:
        unregisterJobWaiter(jobID, resultCh)
        jobStore.Lock()
        delete(jobStore.jobs, jobID)
        jobStore.Unlock()
        atomic.AddInt64(&queuedJobs, -1)
        http.Error(w, "Server busy, please try again later.", http.StatusServiceUnavailable)
    }
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
    enableCORS(w)

    if r.Method == http.MethodOptions {
        w.WriteHeader(http.StatusOK)
        return
    }

    jobID := filepath.Base(r.URL.Path)
    if jobID == "" {
        http.Error(w, "Missing job ID", http.StatusBadRequest)
        return
    }

    job, err := getJobFromRedis(jobID)
    if err != nil || job == nil {
        jobStore.RLock()
        jobMem, exists := jobStore.jobs[jobID]
        jobStore.RUnlock()
        if !exists {
            http.Error(w, "Job not found", http.StatusNotFound)
            return
        }
        job = jobMem
    }

    response := struct {
        JobID       string    `json:"job_id"`
        Status      JobStatus `json:"status"`
        Progress    string    `json:"progress,omitempty"`
        DownloadURL string    `json:"download_url,omitempty"`
        Error       string    `json:"error,omitempty"`
        Metadata    *Metadata `json:"metadata,omitempty"`
        CreatedAt   time.Time `json:"created_at"`
        CompletedAt time.Time `json:"completed_at,omitempty"`
    }{
        JobID:       job.ID,
        Status:      job.Status,
        DownloadURL: job.DownloadURL,
        Error:       job.Error,
        Metadata:    job.Metadata,
        CreatedAt:   job.CreatedAt,
        CompletedAt: job.CompletedAt,
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(response)
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
    enableCORS(w)

    if r.Method == http.MethodOptions {
        w.WriteHeader(http.StatusOK)
        return
    }

    filenameWithExt := filepath.Base(r.URL.Path)
    jobID := filenameWithExt[:len(filenameWithExt)-len(".mp3")]

    job, err := getJobFromRedis(jobID)
    if err != nil || job == nil {
        jobStore.RLock()
        job, exists := jobStore.jobs[jobID]
        jobStore.RUnlock()
        if !exists || job.Status != StatusCompleted {
            http.Error(w, "File not found or conversion not completed", http.StatusNotFound)
            return
        }
    }

    if job.FilePath == "" {
        http.Error(w, "File path not available", http.StatusInternalServerError)
        return
    }

    file, err := os.Open(job.FilePath)
    if err != nil {
        http.Error(w, "Error opening file", http.StatusInternalServerError)
        return
    }
    defer file.Close()

    w.Header().Set("Content-Type", "audio/mpeg")
    w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filenameWithExt))
    w.Header().Set("Cache-Control", "public, max-age=3600")
    io.Copy(w, file)

    // Schedule deletion 10 minutes after first successful download
    if job.FirstDownloadedAt.IsZero() {
        job.FirstDownloadedAt = time.Now()
        saveJobToRedis(job)
        go func(j *ConversionJob) {
            time.Sleep(10 * time.Minute)
            // Re-check file exists and delete
            if j.FilePath != "" {
                _ = os.Remove(j.FilePath)
            }
            // Remove from store
            jobStore.Lock()
            delete(jobStore.jobs, j.ID)
            jobStore.Unlock()
        }(job)
    }
}
