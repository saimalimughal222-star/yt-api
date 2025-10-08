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
    "strings"
    "strconv"
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
    if req.URL == "" || len(req.URL) > MaxURLLength {
        http.Error(w, "Missing YouTube URL", http.StatusBadRequest)
        return
    }

    if !isValidYouTubeURL(req.URL) {
        http.Error(w, "Invalid YouTube URL", http.StatusBadRequest)
        return
    }

    // Canonicalize URL (supports Shorts, embed, youtu.be, mobile)
    var videoID string
    if canon, ok := canonicalizeYouTubeURL(req.URL); ok {
        req.URL = canon
        if vid, ok2 := extractYouTubeVideoID(canon); ok2 {
            videoID = vid
        }
    }

    // Idempotency key check
    if req.IdempotencyKey != "" {
        if jid, err := getJobIDByIdempotency(req.IdempotencyKey); err == nil && jid != "" {
            if j, err2 := getJobFromRedis(jid); err2 == nil && j != nil {
                w.Header().Set("Content-Type", "application/json")
                json.NewEncoder(w).Encode(map[string]interface{}{
                    "job_id": j.ID,
                    "status": string(j.Status),
                    "download_url": j.DownloadURL,
                    "check_status_endpoint": fmt.Sprintf("http://localhost:8080/status/%s", j.ID),
                    "canonical_url": j.URL,
                })
                return
            }
        }
    }

    // Prefer Redis-based dedupe first
    if jobIDFromURL, err := getJobIDByURL(req.URL); err == nil && jobIDFromURL != "" {
        if jobByRedis, err2 := getJobFromRedis(jobIDFromURL); err2 == nil && jobByRedis != nil && jobByRedis.Status == StatusCompleted {
            w.Header().Set("Content-Type", "application/json")
            json.NewEncoder(w).Encode(map[string]string{
                "job_id": jobByRedis.ID,
                "status": string(jobByRedis.Status),
                "download_url": jobByRedis.DownloadURL,
                "check_status_endpoint": fmt.Sprintf("http://localhost:8080/status/%s", jobByRedis.ID),
            })
            return
        }
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
        VideoID:    videoID,
        Status:     StatusPending,
        CreatedAt:  time.Now(),
        MaxRetries: MaxJobRetries,
        Priority:   1,
        CallbackURL: req.CallbackURL,
    }

    jobStore.Lock()
    jobStore.jobs[jobID] = job
    jobStore.Unlock()

    saveJobToRedis(job)
    _ = saveURLMapping(req.URL, jobID)
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
                    "canonical_url": job.URL,
                })
            } else {
                json.NewEncoder(w).Encode(map[string]interface{}{
                    "job_id": jobID,
                    "status": string(doneJob.Status),
                    "error": doneJob.Error,
                    "check_status_endpoint": fmt.Sprintf("http://localhost:8080/status/%s", jobID),
                    "canonical_url": job.URL,
                })
            }
        case <-time.After(FastPathWait):
            unregisterJobWaiter(jobID, resultCh)
            json.NewEncoder(w).Encode(map[string]string{
                "job_id": jobID,
                "status": string(job.Status),
                "check_status_endpoint": fmt.Sprintf("http://localhost:8080/status/%s", jobID),
                "canonical_url": job.URL,
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
    if !strings.HasSuffix(filenameWithExt, ".mp3") {
        http.Error(w, "Invalid filename", http.StatusBadRequest)
        return
    }
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

    // Mark download in progress to avoid deletion during streaming
    downloadTrackers.Lock()
    downloadTrackers.inProgress[job.ID]++
    downloadTrackers.Unlock()

    file, err := os.Open(job.FilePath)
    if err != nil {
        downloadTrackers.Lock()
        downloadTrackers.inProgress[job.ID]--
        if downloadTrackers.inProgress[job.ID] <= 0 { delete(downloadTrackers.inProgress, job.ID) }
        downloadTrackers.Unlock()
        http.Error(w, "Error opening file", http.StatusInternalServerError)
        return
    }
    defer func() {
        file.Close()
        downloadTrackers.Lock()
        downloadTrackers.inProgress[job.ID]--
        if downloadTrackers.inProgress[job.ID] <= 0 { delete(downloadTrackers.inProgress, job.ID) }
        downloadTrackers.Unlock()
    }()

    // Range support
    fi, _ := file.Stat()
    size := fi.Size()
    w.Header().Set("Accept-Ranges", "bytes")
    w.Header().Set("Content-Type", "audio/mpeg")
    w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filenameWithExt))
    w.Header().Set("Cache-Control", "public, max-age=3600")

    if rng := r.Header.Get("Range"); rng != "" {
        // Simple bytes=START-
        if strings.HasPrefix(rng, "bytes=") {
            parts := strings.TrimPrefix(rng, "bytes=")
            if strings.HasSuffix(parts, "-") {
                startStr := strings.TrimSuffix(parts, "-")
                if start, err := strconv.ParseInt(startStr, 10, 64); err == nil && start < size {
                    w.WriteHeader(http.StatusPartialContent)
                    w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, size-1, size))
                    file.Seek(start, 0)
                    io.Copy(w, file)
                    return
                }
            }
        }
    }

    // Full body
    w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
    io.Copy(w, file)

    // Schedule deletion 10 minutes after first successful download
    if job.FirstDownloadedAt.IsZero() {
        job.FirstDownloadedAt = time.Now()
        saveJobToRedis(job)
        // Schedule deletion if not already scheduled
        downloadTrackers.Lock()
        already := downloadTrackers.scheduled[job.ID]
        if !already { downloadTrackers.scheduled[job.ID] = true }
        downloadTrackers.Unlock()
        if !already {
            go scheduleSafeDeletion(job)
        }
    }
}
