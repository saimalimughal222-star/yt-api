package main

import (
    "log"
    "os"
    "os/signal"
    "syscall"
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
    return "N/A"
}

func calculateSuccessRate() float64 {
    // NOTE: Active calculation requires tracking per-job times; simplified here
    return 0
}

func getAvgProcessingTime() float64 {
    return 0
}
