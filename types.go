package main

import "time"

type Metadata struct {
    Title    string  `json:"title"`
    Uploader string  `json:"uploader"`
    Duration float64 `json:"duration"`
    AudioURL string  `json:"audio_url"`
    Ext      string  `json:"ext"`
    Abr      int     `json:"abr"`
}

type Request struct {
    URL          string `json:"url"`
    CaptchaToken string `json:"captcha_token,omitempty"`
    IdempotencyKey string `json:"idempotency_key,omitempty"`
    CallbackURL   string `json:"callback_url,omitempty"`
}

type JobStatus string

const (
    StatusPending    JobStatus = "pending"
    StatusProcessing JobStatus = "processing"
    StatusCompleted  JobStatus = "completed"
    StatusFailed     JobStatus = "failed"
)

type ConversionJob struct {
    ID          string     `json:"id"`
    URL         string     `json:"url"`
    VideoID     string     `json:"video_id"`
    Status      JobStatus  `json:"status"`
    CreatedAt   time.Time  `json:"created_at"`
    StartedAt   time.Time  `json:"started_at"`
    CompletedAt time.Time  `json:"completed_at"`
    FilePath    string     `json:"file_path"`
    DownloadURL string     `json:"download_url"`
    FirstDownloadedAt time.Time `json:"first_downloaded_at"`
    Error       string     `json:"error"`
    Metadata    *Metadata  `json:"metadata"`
    Retries     int        `json:"retries"`
    MaxRetries  int        `json:"max_retries"`
    Priority    int        `json:"priority"`
    CallbackURL string     `json:"callback_url,omitempty"`
}

type HealthStatus struct {
    Status        string `json:"status"`
    ActiveJobs    int64  `json:"active_jobs"`
    QueuedJobs    int64  `json:"queued_jobs"`
    CompletedJobs int64  `json:"completed_jobs"`
    FailedJobs    int64  `json:"failed_jobs"`
    Workers       int    `json:"workers"`
    Uptime        string `json:"uptime"`
    MemoryUsage   string `json:"memory_usage"`
}
