package main

import (
    "fmt"
    "log"
    "os"
    "path/filepath"
    "sync/atomic"
    "time"
)

func startWorker(workerID int) {
    fmt.Printf("Worker %d started.\n", workerID)
    for job := range jobQueue {
        processJob(job, workerID)
    }
}

func processJob(job *ConversionJob, workerID int) {
    atomic.AddInt64(&activeJobs, 1)
    atomic.AddInt64(&queuedJobs, -1)

    log.Printf("Worker %d: Processing job %s for URL: %s\n", workerID, job.ID, job.URL)

    updateJobStatus(job, StatusProcessing, "")
    job.StartedAt = time.Now()

    outputDir := "downloads"
    if err := os.MkdirAll(outputDir, os.ModePerm); err != nil {
        updateJobStatus(job, StatusFailed, fmt.Sprintf("Error creating downloads directory: %v", err))
        notifyJobCompletion(job)
        atomic.AddInt64(&activeJobs, -1)
        atomic.AddInt64(&failedJobs, 1)
        return
    }
    outputPath := filepath.Join(outputDir, job.ID+".mp3")

    audioURL, meta, err := getAudioStreamFromYTDLP(job.URL)
    if err != nil {
        handleJobFailure(job, err, "yt-dlp stream extraction failed")
        atomic.AddInt64(&activeJobs, -1)
        atomic.AddInt64(&failedJobs, 1)
        return
    }

    if err := convertStreamToMP3(audioURL, outputPath); err != nil {
        handleJobFailure(job, err, "ffmpeg conversion failed")
        atomic.AddInt64(&activeJobs, -1)
        atomic.AddInt64(&failedJobs, 1)
        return
    }

    job.Status = StatusCompleted
    job.CompletedAt = time.Now()
    job.FilePath = outputPath
    job.DownloadURL = fmt.Sprintf("http://localhost:8080/download/%s.mp3", job.ID)
    job.Metadata = meta
    job.Error = ""

    saveJobToRedis(job)

    atomic.AddInt64(&activeJobs, -1)
    atomic.AddInt64(&completedJobs, 1)

    notifyJobCompletion(job)
    log.Printf("Worker %d: Job %s completed successfully. Download: %s\n", workerID, job.ID, job.DownloadURL)
}
