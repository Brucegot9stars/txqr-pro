package sender

import (
	"fmt"

	"github.com/divan/txqr"
	"github.com/divan/txqr/internal/version"
	"github.com/divan/txqr/qr"
)

// UI limits come from the same constants as the request and worker checks.
func (s *Server) pageConfig() map[string]any {
	return map[string]any{
		"version":              version.Number,
		"maxUploadBytes":       min(s.maxUpload, int64(txqr.MaxTransferBytes)),
		"maxSourceBlocks":      txqr.MaxSourceBlocks,
		"maxChunkBytes":        txqr.MaxChunkBytes,
		"qrFrameBytes":         qr.MaxMediumBytes,
		"colorFrameBytes":      qr.NumColorPayloadBytes(),
		"colorSplitBytes":      clampColorSplit(txqr.MaxChunkBytes),
		"decimenBlockBytes":    DecimenBlockLen,
		"workerMemoryBytes":    workerMemoryBytes,
		"pngCacheBytes":        maxPNGCacheBytes,
		"sessionCacheBytes":    maxSessionBytes,
		"maxSessions":          maxActiveSessions,
		"encodeTimeoutSeconds": int(encodeTimeout.Seconds()),
	}
}

func validateClassicRequest(total, split int, mode string, codec txqr.CodecType, name string) error {
	e := txqr.NewEncoder(split, codec)
	e.SetFileName(name)
	length, err := e.MaxFrameSize(total)
	if err != nil {
		return err
	}
	capacity := qr.MaxMediumBytes
	if mode == "color" {
		capacity = qr.NumColorPayloadBytes()
	}
	if length > capacity {
		return fmt.Errorf("当前分片 %d 字节，加上协议头后帧长最多 %d 字节，超过 %s 容量 %d 字节；请减小分片。文件还须满足源块数不超过 %d，过大文件需压缩或分卷传输", split, length, mode, capacity, txqr.MaxSourceBlocks)
	}
	return nil
}
