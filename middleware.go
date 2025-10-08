package main

import (
    "net/http"
    "strings"
    "time"
    "golang.org/x/time/rate"
)

func rateLimitMiddleware(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if rateLimiter != nil && !rateLimiter.Allow() {
            http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
            return
        }
        // Per-IP limiter
        ip := r.Header.Get("X-Real-IP")
        if ip == "" {
            ip = r.RemoteAddr
        }
        ipLimiters.Lock()
        lim, ok := ipLimiters.m[ip]
        if !ok {
            lim = rate.NewLimiter(rate.Limit(PerIPRPS), PerIPBurst)
            ipLimiters.m[ip] = lim
        }
        ipLimiters.Unlock()
        if !lim.Allow() {
            http.Error(w, "Per-IP rate limit exceeded", http.StatusTooManyRequests)
            return
        }
        next(w, r)
    }
}

func enableCORS(w http.ResponseWriter) {
    originHeader := "*"
    if AllowedOrigins != "*" {
        originHeader = AllowedOrigins
    }
    w.Header().Set("Access-Control-Allow-Origin", originHeader)
    w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
    w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
    w.Header().Set("X-Content-Type-Options", "nosniff")
}

func apiKeyMiddleware(next http.HandlerFunc) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if !RequireAPIKey {
            next(w, r)
            return
        }
        key := r.Header.Get("X-API-Key")
        if key == "" {
            http.Error(w, "Missing API key", http.StatusUnauthorized)
            return
        }
        if !isValidAPIKey(key) {
            http.Error(w, "Invalid API key", http.StatusUnauthorized)
            return
        }
        next(w, r)
    }
}

func isValidAPIKey(k string) bool {
    if len(apiKeys) == 0 {
        // initialize from CSV once
        if APIKeysCSV == "" {
            return false
        }
        parts := strings.Split(APIKeysCSV, ",")
        for _, p := range parts {
            p = strings.TrimSpace(p)
            if p != "" {
                apiKeys[p] = struct{}{}
            }
        }
    }
    _, ok := apiKeys[k]
    return ok
}

// Simple backoff helper (seconds)
func backoffForRetry(retry int) time.Duration {
    // exponential backoff capped
    sec := BackoffBaseSeconds << (retry - 1)
    if sec > BackoffMaxSeconds {
        sec = BackoffMaxSeconds
    }
    if sec < 1 {
        sec = 1
    }
    return time.Duration(sec) * time.Second
}
