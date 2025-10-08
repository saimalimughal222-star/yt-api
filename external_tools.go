package main

import (
    "bytes"
    "context"
    "encoding/json"
    "fmt"
    "os/exec"
    "sort"
    "strings"
    "time"
)

type ytdlpFormat struct {
    FormatID string  `json:"format_id"`
    ACodec   string  `json:"acodec"`
    VCodec   string  `json:"vcodec"`
    Ext      string  `json:"ext"`
    Protocol string  `json:"protocol"`
    URL      string  `json:"url"`
    ABR      float64 `json:"abr"`
    TBR      float64 `json:"tbr"`
}

type ytdlpInfo struct {
    Title    string        `json:"title"`
    Uploader string        `json:"uploader"`
    Duration float64       `json:"duration"`
    Formats  []ytdlpFormat `json:"formats"`
}

func getAudioStreamFromYTDLP(videoURL string) (string, *Metadata, error) {
    ctxTimeout, cancel := context.WithTimeout(ctx, 45*time.Second)
    defer cancel()

    cmd := exec.CommandContext(ctxTimeout, "yt-dlp", "-J", "--no-warnings", "--skip-download", videoURL)
    var stdout, stderr bytes.Buffer
    cmd.Stdout = &stdout
    cmd.Stderr = &stderr
    if err := cmd.Run(); err != nil {
        return "", nil, fmt.Errorf("yt-dlp metadata error: %v | %s", err, strings.TrimSpace(stderr.String()))
    }

    var info ytdlpInfo
    if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
        return "", nil, fmt.Errorf("yt-dlp metadata parse error: %v", err)
    }

    candidates := make([]ytdlpFormat, 0, len(info.Formats))
    for _, f := range info.Formats {
        if f.URL == "" {
            continue
        }
        isAudioOnly := (f.VCodec == "none" || f.VCodec == "") && f.ACodec != "none"
        if isAudioOnly {
            candidates = append(candidates, f)
            continue
        }
    }
    if len(candidates) == 0 {
        for _, f := range info.Formats {
            if f.URL == "" {
                continue
            }
            if f.ACodec != "none" {
                candidates = append(candidates, f)
            }
        }
    }
    if len(candidates) == 0 {
        return "", nil, fmt.Errorf("no usable audio formats found")
    }

    sort.SliceStable(candidates, func(i, j int) bool {
        si := scoreFormat(formatInfoForScore{Ext: candidates[i].Ext, Protocol: candidates[i].Protocol, ABR: candidates[i].ABR, TBR: candidates[i].TBR})
        sj := scoreFormat(formatInfoForScore{Ext: candidates[j].Ext, Protocol: candidates[j].Protocol, ABR: candidates[j].ABR, TBR: candidates[j].TBR})
        if si == sj {
            return candidates[i].ABR > candidates[j].ABR
        }
        return si > sj
    })

    best := candidates[0]
    meta := &Metadata{
        Title:    info.Title,
        Uploader: info.Uploader,
        Duration: info.Duration,
        AudioURL: best.URL,
        Ext:      best.Ext,
        Abr:      int(best.ABR),
    }
    return best.URL, meta, nil
}

func convertStreamToMP3(audioURL, outputPath string) error {
    ctxTimeout, cancel := context.WithTimeout(ctx, 10*time.Minute)
    defer cancel()

    args := []string{
        "-y",
        "-loglevel", "error",
        "-nostdin",
        "-i", audioURL,
        "-vn",
        "-acodec", "libmp3lame",
        "-ar", "44100",
        "-b:a", "192k",
        outputPath,
    }
    cmd := exec.CommandContext(ctxTimeout, "ffmpeg", args...)
    var stderr bytes.Buffer
    cmd.Stderr = &stderr
    if err := cmd.Run(); err != nil {
        return fmt.Errorf("ffmpeg error: %v | %s", err, strings.TrimSpace(stderr.String()))
    }
    return nil
}

type formatInfoForScore struct {
    Ext      string
    Protocol string
    ABR      float64
    TBR      float64
}

func scoreFormat(f formatInfoForScore) int {
    score := 0
    switch strings.ToLower(f.Ext) {
    case "m4a":
        score += 100
    case "webm":
        score += 90
    case "ogg", "opus":
        score += 85
    case "mp4":
        score += 70
    default:
        score += 60
    }
    p := strings.ToLower(f.Protocol)
    if strings.HasPrefix(p, "https") {
        score += 30
    } else if strings.HasPrefix(p, "http") {
        score += 25
    } else if strings.Contains(p, "m3u8") || strings.Contains(p, "hls") {
        score += 20
    } else if strings.Contains(p, "dash") {
        score += 15
    }
    if f.ABR > 0 {
        score += int(f.ABR)
    } else if f.TBR > 0 {
        score += int(f.TBR / 2)
    }
    return score
}
