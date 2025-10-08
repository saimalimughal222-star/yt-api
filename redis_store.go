package main

import (
    "encoding/json"
    "fmt"
    "log"
    "time"

    redis "github.com/redis/go-redis/v9"
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
    return redisClient.Set(ctx, key, jobData, JobExpirationHours*time.Hour).Err()
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
