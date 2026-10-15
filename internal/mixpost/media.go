package mixpost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultChunkThreshold is the file size above which uploads switch to the
// chunked endpoints. It stays well under the upload limit of a default PHP
// install so a single POST does not get rejected by the web server.
const DefaultChunkThreshold int64 = 8 << 20

// Media is an uploaded file. The API returns the id as a string.
type Media struct {
	ID        json.Number `json:"id"`
	UUID      string      `json:"uuid"`
	Name      string      `json:"name"`
	MimeType  string      `json:"mime_type"`
	Type      string      `json:"type"`
	URL       string      `json:"url"`
	ThumbURL  string      `json:"thumb_url"`
	IsVideo   bool        `json:"is_video"`
	CreatedAt string      `json:"created_at"`
}

// MediaFolder is a folder of the workspace media library. A folder is a
// label: filing a file under one never moves the file.
type MediaFolder struct {
	UUID       string `json:"uuid"`
	Name       string `json:"name"`
	Parent     string `json:"parent"`
	MediaCount int    `json:"media_count"`
}

// MediaFolders returns the workspace's whole folder tree, which the API never
// paginates.
func (c *Client) MediaFolders(ctx context.Context) ([]MediaFolder, error) {
	resp, err := c.Get(ctx, "media/folders", nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Data []MediaFolder `json:"data"`
	}
	if err := resp.DecodeInto(&payload); err != nil {
		return nil, fmt.Errorf("decode media folders: %w", err)
	}
	return payload.Data, nil
}

// UploadOptions controls a media upload.
type UploadOptions struct {
	AltText string
	// Folder is the uuid of the media folder to file the upload in; empty
	// files it at the root.
	Folder string
	// Chunked forces the chunked flow regardless of file size.
	Chunked bool
	// Threshold is the size above which the chunked flow is used.
	Threshold int64
	// Concurrency is how many chunks are uploaded in parallel.
	Concurrency int
	// Progress, when set, receives human-readable status lines.
	Progress func(string)
}

// UploadFile uploads a local file, choosing the single-request or the chunked
// flow based on its size, and returns the raw media JSON.
func (c *Client) UploadFile(ctx context.Context, path string, opts UploadOptions) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory", path)
	}

	threshold := opts.Threshold
	if threshold <= 0 {
		threshold = DefaultChunkThreshold
	}
	chunked := opts.Chunked || info.Size() > threshold

	if c.DryRun {
		return c.uploadPlan(path, info.Size(), chunked, opts)
	}
	if chunked {
		return c.uploadChunked(ctx, path, info.Size(), opts)
	}
	return c.uploadSingle(ctx, path, opts)
}

// uploadPlan describes an upload without performing it, so --dry-run reports
// the endpoints a file would go through and the choice between the two flows.
func (c *Client) uploadPlan(path string, size int64, chunked bool, opts UploadOptions) ([]byte, error) {
	mimeType, err := DetectMIME(path)
	if err != nil {
		return nil, err
	}
	base, err := c.resolveURL(Request{Method: http.MethodPost, Path: "media"})
	if err != nil {
		return nil, err
	}

	plan := map[string]any{
		"dry_run":   true,
		"file":      path,
		"size":      size,
		"mime_type": mimeType,
	}
	if opts.AltText != "" {
		plan["alt_text"] = opts.AltText
	}
	if opts.Folder != "" {
		plan["folder"] = opts.Folder
	}
	if chunked {
		plan["mode"] = "chunked"
		plan["requests"] = []string{
			"POST " + base + "/chunked/initiate",
			"POST " + base + "/chunked/{upload_uuid}/upload (once per chunk, size decided by the instance)",
			"POST " + base + "/chunked/{upload_uuid}/complete",
		}
	} else {
		plan["mode"] = "single"
		plan["requests"] = []string{"POST " + base}
	}
	return json.Marshal(plan)
}

func (c *Client) uploadSingle(ctx context.Context, path string, opts UploadOptions) ([]byte, error) {
	fields := map[string]string{}
	if opts.AltText != "" {
		fields["alt_text"] = opts.AltText
	}
	if opts.Folder != "" {
		fields["folder"] = opts.Folder
	}
	progress(opts, fmt.Sprintf("uploading %s", filepath.Base(path)))

	resp, err := c.Do(ctx, Request{
		Method:      http.MethodPost,
		Path:        "media",
		BodyFactory: fileMultipart(fields, "file", filepath.Base(path), path, 0, 0),
	})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// chunkedSession is the response of the initiate endpoint.
type chunkedSession struct {
	UploadUUID  string `json:"upload_uuid"`
	ChunkSize   int64  `json:"chunk_size"`
	TotalChunks int    `json:"total_chunks"`
}

func (c *Client) uploadChunked(ctx context.Context, path string, size int64, opts UploadOptions) ([]byte, error) {
	name := filepath.Base(path)
	mimeType, err := DetectMIME(path)
	if err != nil {
		return nil, err
	}

	resp, err := c.Call(ctx, http.MethodPost, "media/chunked/initiate", nil, map[string]any{
		"filename":   name,
		"mime_type":  mimeType,
		"total_size": size,
	})
	if err != nil {
		return nil, err
	}
	var session chunkedSession
	if err := resp.DecodeInto(&session); err != nil {
		return nil, fmt.Errorf("decode chunked upload session: %w", err)
	}
	if session.UploadUUID == "" || session.ChunkSize <= 0 || session.TotalChunks <= 0 {
		return nil, fmt.Errorf("chunked upload session is incomplete: %s", string(resp.Body))
	}

	progress(opts, fmt.Sprintf("uploading %s in %d chunks of %s", name, session.TotalChunks, humanBytes(session.ChunkSize)))

	if err := c.uploadChunks(ctx, path, size, session, opts); err != nil {
		// Free the half-finished session on the server; the original error is
		// what the user needs to see, so an abort failure is only traced.
		if abortErr := c.AbortChunkedUpload(context.WithoutCancel(ctx), session.UploadUUID); abortErr != nil {
			c.tracef("abort chunked upload %s: %v\n", session.UploadUUID, abortErr)
		}
		return nil, err
	}

	done, err := c.Call(ctx, http.MethodPost, "media/chunked/"+session.UploadUUID+"/complete", nil, opts.fileData())
	if err != nil {
		return nil, err
	}
	return done.Body, nil
}

func (c *Client) uploadChunks(ctx context.Context, path string, size int64, session chunkedSession, opts UploadOptions) error {
	concurrency := opts.Concurrency
	if concurrency < 1 {
		concurrency = 3
	}
	concurrency = min(concurrency, session.TotalChunks)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		done     int
	)
	sem := make(chan struct{}, concurrency)

	for index := range session.TotalChunks {
		offset := int64(index) * session.ChunkSize
		length := min(session.ChunkSize, size-offset)

		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			if ctx.Err() != nil {
				return
			}
			err := c.uploadChunk(ctx, path, session, index, offset, length)

			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("chunk %d/%d: %w", index+1, session.TotalChunks, err)
					cancel()
				}
				return
			}
			done++
			progress(opts, fmt.Sprintf("chunk %d/%d uploaded", done, session.TotalChunks))
		}()
	}
	wg.Wait()

	return firstErr
}

func (c *Client) uploadChunk(ctx context.Context, path string, session chunkedSession, index int, offset, length int64) error {
	fields := map[string]string{
		"upload_session": session.UploadUUID,
		"chunk_index":    fmt.Sprint(index),
	}
	chunkName := fmt.Sprintf("%s.part%d", filepath.Base(path), index)

	_, err := c.Do(ctx, Request{
		Method:      http.MethodPost,
		Path:        "media/chunked/" + session.UploadUUID + "/upload",
		BodyFactory: fileMultipart(fields, "chunk", chunkName, path, offset, length),
	})
	return err
}

// AbortChunkedUpload discards a chunked upload session.
func (c *Client) AbortChunkedUpload(ctx context.Context, uploadUUID string) error {
	_, err := c.Call(ctx, http.MethodDelete, "media/chunked/"+uploadUUID, nil, nil)
	return err
}

// RemoteUpload asks the instance to fetch a file from a URL. Small files come
// back completed; larger ones are queued and reported through a download id.
type RemoteUpload struct {
	Status     string          `json:"status"`
	DownloadID string          `json:"download_id"`
	Progress   int             `json:"progress"`
	Error      string          `json:"error"`
	Media      json.RawMessage `json:"media"`
}

// fileData is the JSON body of the file details an upload carries alongside
// the file itself.
func (opts UploadOptions) fileData() map[string]any {
	body := map[string]any{}
	if opts.AltText != "" {
		body["alt_text"] = opts.AltText
	}
	if opts.Folder != "" {
		body["folder"] = opts.Folder
	}
	return body
}

// RemoteUploadInitiate starts a remote download. In a dry run the raw payload
// is the request that would have been sent.
func (c *Client) RemoteUploadInitiate(ctx context.Context, fileURL string, opts UploadOptions) (*RemoteUpload, []byte, error) {
	body := opts.fileData()
	body["url"] = fileURL
	resp, err := c.Call(ctx, http.MethodPost, "media/remote/initiate", nil, body)
	if err != nil {
		return nil, nil, err
	}
	if resp.Status == 0 {
		planned, err := json.Marshal(resp.Request)
		return &RemoteUpload{}, planned, err
	}
	var out RemoteUpload
	if err := resp.DecodeInto(&out); err != nil {
		return nil, nil, fmt.Errorf("decode remote upload: %w", err)
	}
	return &out, resp.Body, nil
}

// RemoteUploadStatus polls a queued remote download.
func (c *Client) RemoteUploadStatus(ctx context.Context, downloadID string) (*RemoteUpload, []byte, error) {
	resp, err := c.Get(ctx, "media/remote/"+downloadID+"/status", nil)
	if err != nil {
		return nil, nil, err
	}
	var out RemoteUpload
	if err := resp.DecodeInto(&out); err != nil {
		return nil, nil, fmt.Errorf("decode remote upload status: %w", err)
	}
	return &out, resp.Body, nil
}

// ErrRemoteUploadFailed is returned when the instance could not fetch the file.
var ErrRemoteUploadFailed = errors.New("remote upload failed")

// WaitRemoteUpload polls until the download completes, fails, or ctx expires.
func (c *Client) WaitRemoteUpload(ctx context.Context, downloadID string, interval time.Duration, onProgress func(*RemoteUpload)) ([]byte, error) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	for {
		status, raw, err := c.RemoteUploadStatus(ctx, downloadID)
		if err != nil {
			return nil, err
		}
		if onProgress != nil {
			onProgress(status)
		}
		switch status.Status {
		case "completed":
			if len(status.Media) > 0 {
				return status.Media, nil
			}
			return raw, nil
		case "failed":
			msg := status.Error
			if msg == "" {
				msg = "the instance reported no reason"
			}
			return nil, fmt.Errorf("%w: %s", ErrRemoteUploadFailed, msg)
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// fileMultipart returns a body factory that streams a multipart form built
// from text fields plus a slice of a file. A length of 0 means "to the end".
// The factory is called once per attempt so retries resend the whole part.
func fileMultipart(fields map[string]string, fileField, fileName, path string, offset, length int64) func() (io.Reader, string, error) {
	return func() (io.Reader, string, error) {
		file, err := os.Open(path)
		if err != nil {
			return nil, "", err
		}

		pr, pw := io.Pipe()
		writer := multipart.NewWriter(pw)

		go func() {
			defer file.Close()

			err := func() error {
				for name, value := range fields {
					if err := writer.WriteField(name, value); err != nil {
						return err
					}
				}
				part, err := writer.CreateFormFile(fileField, fileName)
				if err != nil {
					return err
				}

				var src io.Reader = file
				if offset > 0 || length > 0 {
					size := length
					if size <= 0 {
						info, err := file.Stat()
						if err != nil {
							return err
						}
						size = info.Size() - offset
					}
					src = io.NewSectionReader(file, offset, size)
				}
				if _, err := io.Copy(part, src); err != nil {
					return err
				}
				return writer.Close()
			}()

			// Closing the pipe with the error surfaces it on the reading side,
			// which is what the HTTP transport is holding.
			pw.CloseWithError(err)
		}()

		return pr, writer.FormDataContentType(), nil
	}
}

// DetectMIME resolves a file's media type from its extension, falling back to
// sniffing the first bytes of content.
func DetectMIME(path string) (string, error) {
	if ext := filepath.Ext(path); ext != "" {
		if t := mime.TypeByExtension(ext); t != "" {
			return strings.TrimSpace(strings.Split(t, ";")[0]), nil
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	head := make([]byte, 512)
	n, err := file.Read(head)
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(strings.Split(http.DetectContentType(head[:n]), ";")[0]), nil
}

func progress(opts UploadOptions, msg string) {
	if opts.Progress != nil {
		opts.Progress(msg)
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// HumanBytes formats a byte count for progress output.
func HumanBytes(n int64) string { return humanBytes(n) }
