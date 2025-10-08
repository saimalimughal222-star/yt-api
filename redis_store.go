package main

import (
    "encoding/json"
    "fmt"
    "log"
    "time"

    redis "github.com/redis/go-redis/v9"
    xxhash "github.com/cespare/xxhash/v2"
)

func initRedis() {
    redisClient = redis.NewClient(&redis.Options{
        Addr:     RedisAddr,
        Password: RedisPassword,
        DB:       RedisDB,
    })
    if _, err := redisClient.Ping(ctx).Result(); err != nil {
        log.Printf("⚠️  Redis not available, using in-memory storage: %v", err)
        redisClient = nil
    } else {
        log.Println("✅ Redis connected successfully")
    }
}

func saveJobToRedis(job *ConversionJob) error {
    if redisClient == nil {
        return nil
    }
    jobData, err := json.Marshal(job)
    if err != nil {
        return err
    }
    key := fmt.Sprintf("job:%s", job.ID)
    expiration := time.Duration(JobExpirationHours) * time.Hour
    return redisClient.Set(ctx, key, jobData, expiration).Err()
}

func getJobFromRedis(jobID string) (*ConversionJob, error) {
    if redisClient == nil {
        return nil, nil
    }
    key := fmt.Sprintf("job:%s", jobID)
    val, err := redisClient.Get(ctx, key).Result()
    if err != nil {
        return nil, err
    }
    var job ConversionJob
    if err := json.Unmarshal([]byte(val), &job); err != nil {
        return nil, err
    }
    return &job, nil
}

// URL mapping for deduplication across restarts
func saveURLMapping(videoURL, jobID string) error {
    if redisClient == nil {
        return nil
    }
    key := fmt.Sprintf("url:%x", xxhashString(videoURL))
    expiration := time.Duration(JobExpirationHours) * time.Hour
    return redisClient.Set(ctx, key, jobID, expiration).Err()
}

func getJobIDByURL(videoURL string) (string, error) {
    if redisClient == nil {
        return "", nil
    }
    key := fmt.Sprintf("url:%x", xxhashString(videoURL))
    return redisClient.Get(ctx, key).Result()
}

func removeURLMapping(videoURL string) {
    if redisClient == nil {
        return
    }
    key := fmt.Sprintf("url:%x", xxhashString(videoURL))
    _ = redisClient.Del(ctx, key).Err()
}

func deleteJobFromRedis(jobID string) {
    if redisClient == nil {
        return
    }
    key := fmt.Sprintf("job:%s", jobID)
    _ = redisClient.Del(ctx, key).Err()
}

func xxhashString(s string) uint64 {
    return xxhash.Sum64String(s)
}
