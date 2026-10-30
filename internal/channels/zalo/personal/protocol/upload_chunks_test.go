package protocol

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type recordedChunk struct {
	ChunkID    int
	TotalChunk int
	TotalSize  int
	Size       int
}

// fakeZaloFileServer mimics the Zalo file service: it decrypts each chunk's params,
// enforces a per-chunk size cap, and answers with an encrypted response envelope.
func fakeZaloFileServer(t *testing.T, key []byte, maxChunk int) (*httptest.Server, func() []recordedChunk) {
	t.Helper()
	var mu sync.Mutex
	var chunks []recordedChunk

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plain, err := DecodeAESCBC(key, r.URL.Query().Get("params"))
		if err != nil {
			t.Errorf("decrypt params: %v", err)
			return
		}
		var params struct {
			ChunkID    int `json:"chunkId"`
			TotalChunk int `json:"totalChunk"`
			TotalSize  int `json:"totalSize"`
		}
		_ = json.Unmarshal(plain, &params)

		file, _, err := r.FormFile("chunkContent")
		if err != nil {
			t.Errorf("chunkContent: %v", err)
			return
		}
		data, _ := io.ReadAll(file)

		mu.Lock()
		chunks = append(chunks, recordedChunk{params.ChunkID, params.TotalChunk, params.TotalSize, len(data)})
		mu.Unlock()

		inner := `{"error_code":201,"error_message":"chunk too large"}`
		if len(data) <= maxChunk {
			finished := 0
			if params.ChunkID == params.TotalChunk {
				finished = 1
			}
			inner = fmt.Sprintf(`{"error_code":0,"data":{"photoId":"%d","normalUrl":"https://img/%d","fileId":"%d","chunkId":%d,"finished":%d}}`,
				1000+params.ChunkID, params.ChunkID, 2000+params.ChunkID, params.ChunkID, finished)
		}
		enc, _ := EncodeAESCBC(key, inner, false)
		_ = json.NewEncoder(w).Encode(map[string]any{"error_code": 0, "data": enc})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []recordedChunk {
		mu.Lock()
		defer mu.Unlock()
		return append([]recordedChunk(nil), chunks...)
	}
}

func newUploadTestSession(t *testing.T, fileServiceURL string, settings *Settings) (*Session, []byte) {
	t.Helper()
	key := bytes.Repeat([]byte{7}, 16)
	sess := NewSession()
	sess.IMEI = "test-imei"
	sess.SecretKey = base64.StdEncoding.EncodeToString(key)
	sess.Settings = settings
	sess.LoginInfo = &LoginInfo{ZpwServiceMapV3: ZpwServiceMapV3{File: []string{fileServiceURL}}}
	return sess, key
}

func TestUploadImageSplitsIntoChunksUnderLimit(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 16)
	srv, recorded := fakeZaloFileServer(t, key, defaultUploadChunkSize)
	sess, _ := newUploadTestSession(t, srv.URL, nil)

	size := 3_533_683 // the PNG that Zalo rejected in production
	path := filepath.Join(t.TempDir(), "big.png")
	if err := os.WriteFile(path, bytes.Repeat([]byte{1}, size), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := UploadImage(t.Context(), sess, "thread-1", ThreadTypeUser, path)
	if err != nil {
		t.Fatalf("UploadImage() error = %v", err)
	}

	got := recorded()
	wantChunks := (size + defaultUploadChunkSize - 1) / defaultUploadChunkSize
	if len(got) != wantChunks {
		t.Fatalf("chunks sent = %d, want %d", len(got), wantChunks)
	}
	total := 0
	for i, c := range got {
		if c.ChunkID != i+1 || c.TotalChunk != wantChunks || c.TotalSize != size {
			t.Fatalf("chunk %d params = %+v, want chunkId=%d totalChunk=%d totalSize=%d", i, c, i+1, wantChunks, size)
		}
		if c.Size > defaultUploadChunkSize {
			t.Fatalf("chunk %d size = %d, exceeds %d", i, c.Size, defaultUploadChunkSize)
		}
		total += c.Size
	}
	if total != size {
		t.Fatalf("bytes uploaded = %d, want %d", total, size)
	}
	if want := fmt.Sprintf("%d", 1000+wantChunks); result.PhotoID.String() != want {
		t.Fatalf("PhotoID = %q, want %q (last chunk response)", result.PhotoID, want)
	}
	if result.TotalSize != size {
		t.Fatalf("TotalSize = %d, want %d", result.TotalSize, size)
	}
}

func TestUploadImageSmallFileIsSingleChunk(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 16)
	srv, recorded := fakeZaloFileServer(t, key, defaultUploadChunkSize)
	sess, _ := newUploadTestSession(t, srv.URL, nil)

	path := filepath.Join(t.TempDir(), "small.png")
	if err := os.WriteFile(path, []byte("tiny"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := UploadImage(t.Context(), sess, "thread-1", ThreadTypeUser, path); err != nil {
		t.Fatalf("UploadImage() error = %v", err)
	}
	if got := recorded(); len(got) != 1 || got[0].ChunkID != 1 || got[0].TotalChunk != 1 {
		t.Fatalf("chunks = %+v, want a single chunk 1/1", got)
	}
}

func TestUploadChunkSizeHonoursServerSetting(t *testing.T) {
	settings := &Settings{Features: Features{Sharefile: SharefileSettings{ChunkSizeFile: 100}}}
	if got := uploadChunkSize(&Session{Settings: settings}); got != 100 {
		t.Fatalf("uploadChunkSize() = %d, want 100 from settings", got)
	}
	if got := uploadChunkSize(&Session{}); got != defaultUploadChunkSize {
		t.Fatalf("uploadChunkSize() = %d, want default %d", got, defaultUploadChunkSize)
	}
}
