package protocol

import (
	"context"
	"fmt"
	"maps"
	"net/http"
)

// defaultUploadChunkSize is used when the login settings omit
// features.sharefile.chunk_size_file. Zalo rejects larger chunks with
// inner error 201 ("chunk upload must not exceed 512K"); 512000 bytes is safe
// whether the server means 512*1000 or 512*1024.
const defaultUploadChunkSize = 512 * 1000

// uploadChunkSize returns the server-advertised chunk size, falling back to the default.
func uploadChunkSize(sess *Session) int {
	if sess.Settings != nil && sess.Settings.Features.Sharefile.ChunkSizeFile > 0 {
		return sess.Settings.Features.Sharefile.ChunkSizeFile
	}
	return defaultUploadChunkSize
}

// uploadChunks POSTs data to endpoint as sequential multipart "chunkContent" parts,
// each with its own encrypted params (totalChunk, chunkId 1..N), matching the
// zca-js/zcago upload flow. baseParams must hold every field except the chunk
// counters. Returns the decrypted data field of the last chunk's response, which
// carries the completed upload metadata (photoId/normalUrl or fileId).
func uploadChunks(ctx context.Context, sess *Session, endpoint, typeParam, fileName string, baseParams map[string]any, data []byte) ([]byte, error) {
	chunkSize := uploadChunkSize(sess)
	totalChunk := max(1, (len(data)+chunkSize-1)/chunkSize)

	var last []byte
	for i := range totalChunk {
		start := i * chunkSize
		end := min(start+chunkSize, len(data))

		params := maps.Clone(baseParams)
		params["totalChunk"] = totalChunk
		params["chunkId"] = i + 1

		plain, err := uploadOneChunk(ctx, sess, endpoint, typeParam, fileName, params, data[start:end])
		if err != nil {
			return nil, fmt.Errorf("chunk %d/%d: %w", i+1, totalChunk, err)
		}
		last = plain
	}
	return last, nil
}

func uploadOneChunk(ctx context.Context, sess *Session, endpoint, typeParam, fileName string, params map[string]any, chunk []byte) ([]byte, error) {
	encParams, err := encryptPayload(sess, params)
	if err != nil {
		return nil, fmt.Errorf("encrypt upload params: %w", err)
	}
	uploadURL := makeURL(sess, endpoint, map[string]any{
		"type":   typeParam,
		"params": encParams,
	}, true)

	body, contentType, err := buildMultipartBody("chunkContent", fileName, chunk)
	if err != nil {
		return nil, fmt.Errorf("build multipart: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, body)
	if err != nil {
		return nil, err
	}
	setDefaultHeaders(req, sess)
	req.Header.Set("Content-Type", contentType)

	resp, err := sess.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upload: %w", err)
	}
	defer resp.Body.Close()

	var envelope Response[*string]
	if err := readJSON(resp, &envelope); err != nil {
		return nil, fmt.Errorf("parse upload response: %w", err)
	}
	if envelope.ErrorCode != 0 {
		return nil, fmt.Errorf("upload error code %d", envelope.ErrorCode)
	}
	if envelope.Data == nil {
		return nil, fmt.Errorf("empty upload response")
	}

	plain, err := decryptDataField(sess, *envelope.Data)
	if err != nil {
		return nil, fmt.Errorf("decrypt upload response: %w", err)
	}
	return plain, nil
}
