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
    "sort"
    "math"
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

    if MaintenanceMode {
        w.Header().Set("Retry-After", "120")
        http.Error(w, "Maintenance mode: new jobs temporarily disabled", http.StatusServiceUnavailable)
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

// DELETE /cancel/{job_id}
func handleCancel(w http.ResponseWriter, r *http.Request) {
    enableCORS(w)
    if r.Method == http.MethodOptions { w.WriteHeader(http.StatusOK); return }
    if r.Method != http.MethodDelete { http.Error(w, "Invalid request method", http.StatusMethodNotAllowed); return }
    jobID := filepath.Base(r.URL.Path)
    if jobID == "" { http.Error(w, "Missing job ID", http.StatusBadRequest); return }
    jobCancels.Lock()
    cancelFn, ok := jobCancels.m[jobID]
    jobCancels.Unlock()
    if !ok {
        http.Error(w, "Job not running or not found", http.StatusNotFound)
        return
    }
    cancelFn()
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"canceled": jobID})
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

// Simple docs pages
func handleDocs(w http.ResponseWriter, r *http.Request) {
    enableCORS(w)
    if r.Method != http.MethodGet { http.Error(w, "Method not allowed", http.StatusMethodNotAllowed); return }
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"><title>YT MP3 API Docs</title><style>body{font-family:sans-serif;max-width:900px;margin:2rem auto;padding:0 1rem;}code{background:#f5f5f5;padding:2px 4px;border-radius:3px}</style></head><body>
    <h1>YouTube to MP3 API - Documentation</h1>
    <p>High-level guide for backend integration.</p>
    <h2>Endpoints</h2>
    <ul>
      <li><code>POST /extract</code> - Start conversion. Body: { url, idempotency_key?, callback_url? }</li>
      <li><code>GET /status/{job_id}</code> - Check job status.</li>
      <li><code>GET /download/{job_id}.mp3</code> - Download MP3 (Range supported).</li>
      <li><code>DELETE /cancel/{job_id}</code> - Cancel a running job.</li>
      <li><code>DELETE /delete/{job_id}</code> - Delete job and file (idempotent).</li>
      <li><code>GET /health</code>, <code>/metrics</code>, <code>/stats</code> - Monitoring.</li>
    </ul>
    <h2>Deployment Guides</h2>
    <ul>
      <li><a href="/README_HIGH_TRAFFIC.md" target="_blank">High-Traffic Tuning</a></li>
      <li><a href="/README_UBUNTU_DEPLOY.md" target="_blank">Ubuntu Deployment</a></li>
    </ul>
    <h2>Auth</h2>
    <p>If enabled, send <code>X-API-Key: &lt;your_key&gt;</code> header.</p>
    <h2>Notes</h2>
    <ul>
      <li>Provide valid YouTube URL (shorts and embed supported).</li>
      <li>Repeated requests for same video are deduped.</li>
      <li>Files are short-lived and may be deleted ~10 minutes after completion.</li>
    </ul>
    <h2>Logs & Troubleshooting</h2>
    <pre>Systemd live:   sudo journalctl -u ytmp3-api -f
Recent hour:    sudo journalctl -u ytmp3-api --since "1 hour ago"
Service status: sudo systemctl status ytmp3-api --no-pager

No-systemd logs: sudo tail -f /opt/ytmp3-api/ytmp3-api.log
PID file:        sudo cat /opt/ytmp3-api/ytmp3-api.pid

Nginx logs:      sudo tail -f /var/log/nginx/access.log /var/log/nginx/error.log

Redis checks:    redis-cli ping
                 redis-cli info | head
    </pre>
    <p>Frontend-focused docs: <a href="/docs/frontend">/docs/frontend</a></p>
    </body></html>`)
}

func handleDocsFrontend(w http.ResponseWriter, r *http.Request) {
    enableCORS(w)
    if r.Method != http.MethodGet { http.Error(w, "Method not allowed", http.StatusMethodNotAllowed); return }
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"><title>Frontend Integration</title><style>body{font-family:sans-serif;max-width:900px;margin:2rem auto;padding:0 1rem;}</style></head><body>
    <h1>Frontend Integration</h1>
    <p>Use fetch with CORS. Example:</p>
    <pre><code>fetch('/extract',{method:'POST',headers:{'Content-Type':'application/json','X-API-Key':'YOUR_KEY'},body:JSON.stringify({url})})
 .then(r=>r.json())
 .then(({job_id})=>pollStatus(job_id))</code></pre>
    <p>Poll status every few seconds. When status = completed, navigate to <code>/download/{job_id}.mp3</code>.</p>
    </body></html>`)
}

// Minimal admin dashboard (basic auth protected)
func handleAdmin(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet { http.Error(w, "Method not allowed", http.StatusMethodNotAllowed); return }
    w.Header().Set("Content-Type", "text/html; charset=utf-8")
    io.WriteString(w, `<!doctype html><html><head><meta charset="utf-8"><title>Admin</title>
    <style>
    :root{--bg:#0f172a;--card:#111827;--muted:#94a3b8;--ok:#10b981;--warn:#f59e0b;--bad:#ef4444;--txt:#e5e7eb}
    body{font-family:system-ui,-apple-system,Segoe UI,Roboto,Ubuntu,Cantarell,Noto Sans,sans-serif;background:var(--bg);color:var(--txt);margin:0}
    header{padding:16px 24px;border-bottom:1px solid #1f2937;display:flex;align-items:center;gap:16px}
    .wrap{max-width:1200px;margin:0 auto;padding:24px}
    .grid{display:grid;grid-template-columns:repeat(4,1fr);gap:16px}
    .card{background:var(--card);border:1px solid #1f2937;border-radius:10px;padding:16px}
    .card h3{margin:0 0 8px 0;font-size:14px;color:var(--muted)}
    .big{font-size:28px;font-weight:600}
    .row{display:flex;gap:16px;align-items:center;flex-wrap:wrap}
    table{width:100%;border-collapse:collapse}
    th,td{border-bottom:1px solid #1f2937;padding:8px;text-align:left;font-size:13px}
    tr:hover td{background:#0b1220}
    input,button,select{background:#0b1220;color:var(--txt);border:1px solid #1f2937;border-radius:8px;padding:8px}
    button{cursor:pointer}
    .ok{color:var(--ok)}.warn{color:var(--warn)}.bad{color:var(--bad)}
    a{color:#60a5fa}
    </style>
    <script>
    async function fetchJSON(u){
      const r = await fetch(u);
      if(!r.ok) throw new Error('http '+r.status);
      return r.json();
    }
    async function postJSON(u, body){
      const r = await fetch(u,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body||{})});
      if(!r.ok) throw new Error('http '+r.status);
      return r.json();
    }
    async function refresh(){
      try{
        const d = await fetchJSON('/admin/data');
        document.getElementById('uptime').textContent = d.health.uptime;
        document.getElementById('workers').textContent = d.health.workers;
        document.getElementById('active').textContent = d.health.active_jobs;
        document.getElementById('queued').textContent = d.health.queued_jobs;
        document.getElementById('completed').textContent = d.health.completed_jobs;
        document.getElementById('failed').textContent = d.health.failed_jobs;
        document.getElementById('rate').textContent = d.metrics.rate_limit;
        document.getElementById('queueCap').textContent = d.metrics.queue_capacity;
        document.getElementById('mem').textContent = d.health.memory_usage;
        document.getElementById('p50').textContent = (d.metrics.p50_processing_s||0).toFixed(2);
        document.getElementById('p95').textContent = (d.metrics.p95_processing_s||0).toFixed(2);
        document.getElementById('cpm').textContent = d.metrics.conversions_per_min||0;
        document.getElementById('storage').textContent = `${d.metrics.files_count||0} files • ${d.metrics.storage_human||'0 B'}`;
        document.getElementById('maintLbl').textContent = d.metrics.maintenance? 'Maintenance ON' : '';
        renderActive(d.active||[]);
        renderRecent(d.recent||[]);
        renderFiles(d.files||[]);
        // legacy list for quick filtering across all jobs snapshot (optional)
        const flat = (d.recent||[]).map(x=>({id:x.id,status:'completed',url:x.url,created_at:x.completed_at}));
        renderJobs(flat);
      }catch(e){console.error(e)}
    }
    function renderActive(items){
      const tbody = document.getElementById('activeJobs');
      tbody.innerHTML='';
      items.forEach(j=>{
        const tr = document.createElement('tr');
        tr.innerHTML = `<td>${j.id}</td><td>${(j.url||'').slice(0,80)}</td><td>${j.started_at}</td><td>${j.elapsed_sec}</td>`;
        tbody.appendChild(tr);
      });
    }
    function renderRecent(items){
      const tbody = document.getElementById('recentJobs');
      tbody.innerHTML='';
      items.forEach(j=>{
        const tr = document.createElement('tr');
        tr.innerHTML = `<td>${j.id}</td><td>${(j.url||'').slice(0,80)}</td><td>${j.completed_at}</td><td>${j.duration_sec}</td><td>${j.download_url?`<a href=\"${j.download_url}\" target=\"_blank\">Download</a>`:''}</td>`;
        tbody.appendChild(tr);
      });
    }
    function renderFiles(items){
      const tbody = document.getElementById('filesTbl');
      if(!tbody) return;
      tbody.innerHTML='';
      items.forEach(f=>{
        const delBtn = f.id? '<button onclick="deleteJob(\''+f.id+'\')">Delete</button>' : '';
        const tr = document.createElement('tr');
        const secs = (f.delete_in_sec||0);
        tr.innerHTML = '<td>'+ (f.name||'') +'</td><td>'+ (f.size_human||'') +'</td><td class="countdown" data-rem="'+secs+'">'+secs+'</td><td>'+delBtn+'</td>';
        tbody.appendChild(tr);
      });
    }
    function tickCountdown(){
      const els = document.querySelectorAll('.countdown');
      els.forEach(el=>{
        let n = parseInt(el.getAttribute('data-rem')||'0',10);
        if (isNaN(n) || n<=0){ el.textContent='0'; return; }
        n = n - 1;
        el.setAttribute('data-rem', String(n));
        el.textContent = String(n);
      });
    }
    function renderJobs(items){
      const q = document.getElementById('q').value.toLowerCase();
      const st = document.getElementById('filter').value;
      const tbody = document.getElementById('jobs');
      tbody.innerHTML='';
      items.forEach(j=>{
        if(q && !(j.id.includes(q)||j.url.includes(q)||j.status.includes(q))) return;
        if(st && j.status!==st) return;
        const tr = document.createElement('tr');
        tr.innerHTML = `<td>${j.id}</td><td>${j.status}</td><td>${(j.url||'').slice(0,80)}</td><td>${j.created_at||''}</td>
        <td><div class="row"><button onclick="cancelJob('${j.id}')">Cancel</button><button onclick="deleteJob('${j.id}')">Delete</button></div></td>`;
        tbody.appendChild(tr);
      });
      document.getElementById('count').textContent=items.length;
    }
    async function cancelJob(id){
      if(!confirm('Cancel job '+id+'?')) return;
      const r = await fetch('/admin/cancel/'+id,{method:'DELETE'});
      if(r.ok) refresh(); else alert('Cancel failed');
    }
    async function deleteJob(id){
      if(!confirm('Delete job '+id+'?')) return;
      const r = await fetch('/admin/delete/'+id,{method:'DELETE'});
      if(r.ok) refresh(); else alert('Delete failed');
    }
    setInterval(refresh, 3000);
    setInterval(tickCountdown, 1000);
    window.onload=refresh;
    </script></head><body>
    <header><h2>Admin Dashboard</h2><div class="row"><a href="/docs">Docs</a><a href="/docs/frontend">Frontend Docs</a></div></header>
    <div class="wrap">
      <div class="grid">
        <div class="card"><h3>Uptime</h3><div class="big" id="uptime">--</div></div>
        <div class="card"><h3>Workers</h3><div class="big" id="workers">--</div></div>
        <div class="card"><h3>Active</h3><div class="big" id="active">--</div></div>
        <div class="card"><h3>Queued</h3><div class="big" id="queued">--</div></div>
        <div class="card"><h3>Completed</h3><div class="big" id="completed">--</div></div>
        <div class="card"><h3>Failed</h3><div class="big" id="failed">--</div></div>
        <div class="card"><h3>Rate Limit</h3><div class="big" id="rate">--</div></div>
        <div class="card"><h3>Queue Capacity</h3><div class="big" id="queueCap">--</div></div>
        <div class="card" style="grid-column: span 4"><h3>Memory</h3><div id="mem">--</div></div>
      </div>
      <div class="grid" style="margin-top:16px">
        <div class="card"><h3>p50 Processing (s)</h3><div class="big" id="p50">--</div></div>
        <div class="card"><h3>p95 Processing (s)</h3><div class="big" id="p95">--</div></div>
        <div class="card"><h3>Conversions / min</h3><div class="big" id="cpm">--</div></div>
        <div class="card"><h3>Storage</h3><div class="big" id="storage">--</div></div>
      </div>
      <div class="card" style="margin-top:16px"><div class="row" style="justify-content:space-between;align-items:center"><div class="row"><button onclick="toggleMaint()">Toggle Maintenance</button><span id="maintLbl" style="margin-left:8px;color:#f59e0b"></span></div></div></div>
      <div class="card" style="margin-top:16px">
        <div class="row" style="justify-content:space-between;align-items:center">
          <div class="row"><input id="q" placeholder="Search jobs (id/url/status)" oninput="refresh()"/><select id="filter" onchange="refresh()"><option value="">All</option><option>pending</option><option>processing</option><option>completed</option><option>failed</option></select></div>
          <div>Total jobs displayed: <span id="count">0</span></div>
        </div>
        <table style="margin-top:8px"><thead><tr><th>ID</th><th>Status</th><th>URL</th><th>Created</th><th>Actions</th></tr></thead><tbody id="jobs"></tbody></table>
      </div>
      <div class="card" style="margin-top:16px">
        <h3>Active Jobs (elapsed seconds)</h3>
        <table><thead><tr><th>ID</th><th>URL</th><th>Started</th><th>Elapsed (s)</th></tr></thead><tbody id="activeJobs"></tbody></table>
      </div>
      <div class="card" style="margin-top:16px">
        <h3>Recent Completed</h3>
        <table><thead><tr><th>ID</th><th>URL</th><th>Completed</th><th>Duration (s)</th><th>Download</th></tr></thead><tbody id="recentJobs"></tbody></table>
      </div>
      <div class="card" style="margin-top:16px">
        <h3>Files</h3>
        <table><thead><tr><th>Name</th><th>Size</th><th>Delete In (s)</th><th>Actions</th></tr></thead><tbody id="filesTbl"></tbody></table>
      </div>
    </div>
    </body></html>`)
}

// Admin data snapshot (health, metrics, stats, jobs limited)
func handleAdminData(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet { http.Error(w, "Method not allowed", http.StatusMethodNotAllowed); return }
    // Health like
    health := HealthStatus{
        Status:        "healthy",
        ActiveJobs:    atomic.LoadInt64(&activeJobs),
        QueuedJobs:    atomic.LoadInt64(&queuedJobs),
        CompletedJobs: atomic.LoadInt64(&completedJobs),
        FailedJobs:    atomic.LoadInt64(&failedJobs),
        Workers:       WorkerPoolSize,
        Uptime:        time.Since(serverStartTime).String(),
        MemoryUsage:   getMemoryUsage(),
    }
    if health.ActiveJobs >= int64(WorkerPoolSize) || health.QueuedJobs > int64(JobQueueCapacity/2) {
        health.Status = "overloaded"
    }
    // Build job views
    jobStore.RLock()
    all := make([]*ConversionJob, 0, len(jobStore.jobs))
    for _, j := range jobStore.jobs { all = append(all, j) }
    jobStore.RUnlock()

    // Active jobs (processing) sorted by StartedAt desc
    type activeDTO struct{ ID, URL, StartedAt string; ElapsedSec int64 }
    act := make([]activeDTO, 0)
    now := time.Now()
    for _, j := range all {
        if j.Status == StatusProcessing && !j.StartedAt.IsZero() {
            act = append(act, activeDTO{ID:j.ID, URL:j.URL, StartedAt:j.StartedAt.Format(time.RFC3339), ElapsedSec:int64(now.Sub(j.StartedAt).Seconds())})
        }
    }
    sort.Slice(act, func(i,j int) bool { return act[i].ElapsedSec > act[j].ElapsedSec })
    if len(act) > 200 { act = act[:200] }

    // Recent completed jobs sorted by CompletedAt desc
    type recentDTO struct{ ID, URL, CompletedAt string; DurationSec int64; DownloadURL string }
    rec := make([]recentDTO, 0)
    durations := make([]float64, 0)
    for _, j := range all {
        if j.Status == StatusCompleted && !j.CompletedAt.IsZero() && !j.StartedAt.IsZero() {
            dur := j.CompletedAt.Sub(j.StartedAt).Seconds()
            durations = append(durations, dur)
            rec = append(rec, recentDTO{ID:j.ID, URL:j.URL, CompletedAt:j.CompletedAt.Format(time.RFC3339), DurationSec:int64(dur), DownloadURL:j.DownloadURL})
        }
    }
    sort.Slice(rec, func(i,j int) bool { return rec[i].CompletedAt > rec[j].CompletedAt })
    if len(rec) > 200 { rec = rec[:200] }

    // Percentiles
    p50, p95, p99 := 0.0, 0.0, 0.0
    if len(durations) > 0 {
        sort.Float64s(durations)
        idx50 := int(math.Ceil(0.50*float64(len(durations)))) - 1; if idx50 < 0 { idx50 = 0 }
        idx95 := int(math.Ceil(0.95*float64(len(durations)))) - 1; if idx95 < 0 { idx95 = 0 }
        idx99 := int(math.Ceil(0.99*float64(len(durations)))) - 1; if idx99 < 0 { idx99 = 0 }
        p50 = durations[idx50]
        p95 = durations[idx95]
        p99 = durations[idx99]
    }

    metrics := map[string]interface{}{
        "active_jobs":    health.ActiveJobs,
        "queued_jobs":    health.QueuedJobs,
        "completed_jobs": health.CompletedJobs,
        "failed_jobs":    health.FailedJobs,
        "workers":        WorkerPoolSize,
        "queue_capacity": JobQueueCapacity,
        "rate_limit":     RequestsPerSecond,
        "burst":          BurstSize,
        "maintenance":    MaintenanceMode,
        "uptime_seconds": time.Since(serverStartTime).Seconds(),
        "success_rate":   calculateSuccessRate(),
        "avg_processing_s": getAvgProcessingTime(),
        "p50_processing_s": p50,
        "p95_processing_s": p95,
        "p99_processing_s": p99,
    }

    // Storage stats (downloads dir)
    downloadsDir := filepath.Join(".", "downloads")
    filesCount := 0
    var totalBytes int64
    type fileDTO struct{ ID, Name string; SizeBytes int64; SizeHuman string; DeleteInSec int64 }
    files := make([]fileDTO, 0)
    if entries, err := os.ReadDir(downloadsDir); err == nil {
        for _, e := range entries {
            if e.IsDir() { continue }
            filesCount++
            fp := filepath.Join(downloadsDir, e.Name())
            if fi, err := os.Stat(fp); err == nil {
                totalBytes += fi.Size()
                // try map to job for countdown
                var id string
                if strings.HasSuffix(e.Name(), ".mp3") { id = strings.TrimSuffix(e.Name(), ".mp3") }
                rem := int64(0)
                if id != "" {
                    jobStore.RLock()
                    j := jobStore.jobs[id]
                    jobStore.RUnlock()
                    if j != nil {
                        delAt := j.CompletedAt.Add(10 * time.Minute)
                        if !j.FirstDownloadedAt.IsZero() { delAt = j.FirstDownloadedAt.Add(10 * time.Minute) }
                        if delAt.After(time.Now()) { rem = int64(delAt.Sub(time.Now()).Seconds()) }
                    }
                }
                files = append(files, fileDTO{ID: id, Name: e.Name(), SizeBytes: fi.Size(), SizeHuman: humanizeBytes(fi.Size()), DeleteInSec: rem})
            }
        }
    }
    metrics["files_count"] = filesCount
    metrics["storage_bytes"] = totalBytes
    metrics["storage_human"] = humanizeBytes(totalBytes)

    // Conversions per minute (last 60s)
    recentConv := 0
    cutoff := time.Now().Add(-60 * time.Second)
    for _, j := range all { if j.Status == StatusCompleted && j.CompletedAt.After(cutoff) { recentConv++ } }
    metrics["conversions_per_min"] = recentConv

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{"health":health,"metrics":metrics,"active":act,"recent":rec,"files":files})
}

// Admin wrappers for actions
func handleAdminCancel(w http.ResponseWriter, r *http.Request) { handleCancel(w,r) }
func handleAdminDelete(w http.ResponseWriter, r *http.Request) { handleDelete(w,r) }

// Admin config: POST {maintenance:'toggle'} or {requests_per_second:int, burst_size:int}
func handleAdminConfig(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost { http.Error(w, "Method not allowed", http.StatusMethodNotAllowed); return }
    var body map[string]interface{}
    if err := json.NewDecoder(r.Body).Decode(&body); err != nil { http.Error(w, "Invalid JSON", http.StatusBadRequest); return }
    if v, ok := body["maintenance"]; ok {
        if s, ok2 := v.(string); ok2 && s == "toggle" {
            MaintenanceMode = !MaintenanceMode
        }
    }
    if v, ok := body["requests_per_second"]; ok {
        if f, ok2 := v.(float64); ok2 && f > 0 {
            RequestsPerSecond = int(f)
        }
    }
    if v, ok := body["burst_size"]; ok {
        if f, ok2 := v.(float64); ok2 && f > 0 {
            BurstSize = int(f)
        }
    }
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{"maintenance":MaintenanceMode,"requests_per_second":RequestsPerSecond,"burst_size":BurstSize})
}

// SSE live logs
func handleAdminLogs(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodGet { http.Error(w, "Method not allowed", http.StatusMethodNotAllowed); return }
    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")
    // emit last lines
    adminLogs.Lock()
    for _, ln := range adminLogs.lines {
        fmt.Fprintf(w, "data: %s\n\n", ln)
    }
    fl, _ := w.(http.Flusher)
    if fl != nil { fl.Flush() }
    ch := subscribeAdminLogs()
    adminLogs.Unlock()
    notify := w.(http.CloseNotifier).CloseNotify()
    for {
        select {
        case ln := <-ch:
            fmt.Fprintf(w, "data: %s\n\n", ln)
            if fl != nil { fl.Flush() }
        case <-notify:
            unsubscribeAdminLogs(ch)
            return
        }
    }
}

// Settings read/write
func handleAdminSettings(w http.ResponseWriter, r *http.Request) {
    if r.Method == http.MethodGet {
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]interface{}{
            "ALLOWED_ORIGINS": AllowedOrigins,
            "REQUESTS_PER_SECOND": RequestsPerSecond,
            "BURST_SIZE": BurstSize,
            "WORKER_POOL_SIZE": WorkerPoolSize,
            "JOB_QUEUE_CAPACITY": JobQueueCapacity,
            "MAX_JOB_RETRIES": MaxJobRetries,
            "JOB_EXPIRATION": JobExpiration.String(),
            "HEALTH_CHECK_INTERVAL": HealthCheckInterval.String(),
            "FAST_PATH_WAIT": FastPathWait.String(),
            "PER_IP_RPS": PerIPRPS,
            "PER_IP_BURST": PerIPBurst,
            "BACKOFF_BASE_SECONDS": BackoffBaseSeconds,
            "BACKOFF_MAX_SECONDS": BackoffMaxSeconds,
            "MAX_DURATION_MIN": os.Getenv("MAX_DURATION_MIN"),
            "ADMIN_USER": AdminUser,
        })
        return
    }
    if r.Method == http.MethodPost {
        var body map[string]interface{}
        if err := json.NewDecoder(r.Body).Decode(&body); err != nil { http.Error(w, "Invalid JSON", http.StatusBadRequest); return }
        if v, ok := body["ALLOWED_ORIGINS"].(string); ok { AllowedOrigins = v }
        if v, ok := body["REQUESTS_PER_SECOND"].(float64); ok { RequestsPerSecond = int(v) }
        if v, ok := body["BURST_SIZE"].(float64); ok { BurstSize = int(v) }
        if v, ok := body["WORKER_POOL_SIZE"].(float64); ok { WorkerPoolSize = int(v) }
        if v, ok := body["JOB_QUEUE_CAPACITY"].(float64); ok { JobQueueCapacity = int(v) }
        if v, ok := body["MAX_JOB_RETRIES"].(float64); ok { MaxJobRetries = int(v) }
        if v, ok := body["PER_IP_RPS"].(float64); ok { PerIPRPS = int(v) }
        if v, ok := body["PER_IP_BURST"].(float64); ok { PerIPBurst = int(v) }
        if v, ok := body["BACKOFF_BASE_SECONDS"].(float64); ok { BackoffBaseSeconds = int(v) }
        if v, ok := body["BACKOFF_MAX_SECONDS"].(float64); ok { BackoffMaxSeconds = int(v) }
        if v, ok := body["ADMIN_USER"].(string); ok { AdminUser = v }
        // NOTE: not setting AdminPass here for safety unless explicitly included
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]string{"status":"ok"})
        return
    }
    http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

// Soft reload: re-init limiter using current config
func handleAdminReload(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost { http.Error(w, "Method not allowed", http.StatusMethodNotAllowed); return }
    rateLimiter = rate.NewLimiter(rate.Limit(RequestsPerSecond), BurstSize)
    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{"reloaded":true,"rps":RequestsPerSecond,"burst":BurstSize})
}
