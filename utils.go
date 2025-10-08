package main

import (
    "log"
    "os"
    "os/signal"
    "syscall"
    "runtime"
    "fmt"
    "strings"
)

func setupGracefulShutdown() {
    c := make(chan os.Signal, 1)
    signal.Notify(c, os.Interrupt, syscall.SIGTERM)
    go func() {
        <-c
        log.Println("🛑 Graceful shutdown initiated...")
        cancel()
        close(jobQueue)
        // Let workers finish gracefully
        log.Println("✅ Graceful shutdown completed")
        os.Exit(0)
    }()
}

func getMemoryUsage() string {
    var m runtime.MemStats
    runtime.ReadMemStats(&m)
    return fmt.Sprintf("Alloc=%d Sys=%d NumGC=%d", m.Alloc, m.Sys, m.NumGC)
}

func calculateSuccessRate() float64 {
    total := completedJobs + failedJobs
    if total <= 0 {
        return 0
    }
    return float64(completedJobs) / float64(total)
}

func getAvgProcessingTime() float64 {
    c := completedJobs
    if c <= 0 {
        return 0
    }
    return float64(totalProcessingTimeNs) / float64(c) / 1e9
}

// Basic YouTube URL validator (covers youtu.be and youtube.com/watch)
func isValidYouTubeURL(u string) bool {
    if strings.HasPrefix(u, "https://www.youtube.com/watch?") ||
        strings.HasPrefix(u, "http://www.youtube.com/watch?") ||
        strings.HasPrefix(u, "https://youtube.com/watch?") ||
        strings.HasPrefix(u, "http://youtube.com/watch?") ||
        strings.HasPrefix(u, "https://youtu.be/") ||
        strings.HasPrefix(u, "http://youtu.be/") {
        return true
    }
    return false
}
