package main

func registerJobWaiter(jobID string) chan *ConversionJob {
    ch := make(chan *ConversionJob, 1)
    jobWaiters.Lock()
    jobWaiters.m[jobID] = append(jobWaiters.m[jobID], ch)
    jobWaiters.Unlock()
    return ch
}

func notifyJobCompletion(job *ConversionJob) {
    jobWaiters.Lock()
    waiters := jobWaiters.m[job.ID]
    delete(jobWaiters.m, job.ID)
    jobWaiters.Unlock()
    for _, ch := range waiters {
        select {
        case ch <- job:
        default:
        }
        // Ensure idempotent close
        safeClose(ch)
    }
}

func unregisterJobWaiter(jobID string, ch chan *ConversionJob) {
    jobWaiters.Lock()
    defer jobWaiters.Unlock()
    waiters := jobWaiters.m[jobID]
    for i, c := range waiters {
        if c == ch {
            jobWaiters.m[jobID] = append(waiters[:i], waiters[i+1:]...)
            break
        }
    }
    if len(jobWaiters.m[jobID]) == 0 {
        delete(jobWaiters.m, jobID)
    }
    safeClose(ch)
}

// safeClose prevents panic on closing an already-closed channel
func safeClose(ch chan *ConversionJob) {
    defer func() { _ = recover() }()
    close(ch)
}
